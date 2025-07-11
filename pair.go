// Copyright (c) 2021 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package whatsmeow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"go.mau.fi/libsignal/ecc"
	"google.golang.org/protobuf/proto"

	waBinary "github.com/oussama-chaabouni/whatsmeow/binary"
	"github.com/oussama-chaabouni/whatsmeow/proto/waAdv"
	"github.com/oussama-chaabouni/whatsmeow/types"
	"github.com/oussama-chaabouni/whatsmeow/types/events"
	"github.com/oussama-chaabouni/whatsmeow/util/keys"
)

var (
	AdvPrefixAccountSignature                                = []byte{6, 0}
	AdvPrefixDeviceSignatureGenerate                         = []byte{6, 1}
	AdvHostedPrefixDeviceIdentityAccountSignature            = []byte{6, 5}
	AdvHostedPrefixDeviceIdentityDeviceSignatureVerification = []byte{6, 6}
)

func (cli *Client) handleIQ(node *waBinary.Node) {
	children := node.GetChildren()
	if len(children) != 1 || node.Attrs["from"] != types.ServerJID {
		return
	}
	switch children[0].Tag {
	case "pair-device":
		cli.handlePairDevice(node)
	case "pair-success":
		cli.handlePairSuccess(node)
	}
}

func (cli *Client) handlePairDevice(node *waBinary.Node) {
	pairDevice := node.GetChildByTag("pair-device")
	err := cli.sendNode(waBinary.Node{
		Tag: "iq",
		Attrs: waBinary.Attrs{
			"to":   node.Attrs["from"],
			"id":   node.Attrs["id"],
			"type": "result",
		},
	})
	if err != nil {
		cli.Log.Warnf("Failed to send acknowledgement for pair-device request: %v", err)
	}

	evt := &events.QR{Codes: make([]string, 0, len(pairDevice.GetChildren()))}
	for i, child := range pairDevice.GetChildren() {
		if child.Tag != "ref" {
			cli.Log.Warnf("pair-device node contains unexpected child tag %s at index %d", child.Tag, i)
			continue
		}
		content, ok := child.Content.([]byte)
		if !ok {
			cli.Log.Warnf("pair-device node contains unexpected child content type %T at index %d", child, i)
			continue
		}
		evt.Codes = append(evt.Codes, cli.makeQRData(string(content)))
	}

	cli.dispatchEvent(evt)
}

func (cli *Client) makeQRData(ref string) string {
	noise := base64.StdEncoding.EncodeToString(cli.Store.NoiseKey.Pub[:])
	identity := base64.StdEncoding.EncodeToString(cli.Store.IdentityKey.Pub[:])
	adv := base64.StdEncoding.EncodeToString(cli.Store.AdvSecretKey)
	return strings.Join([]string{ref, noise, identity, adv}, ",")
}

func (cli *Client) handlePairSuccess(node *waBinary.Node) {
	id := node.Attrs["id"].(string)
	pairSuccess := node.GetChildByTag("pair-success")

	deviceIdentityBytes, _ := pairSuccess.GetChildByTag("device-identity").Content.([]byte)
	businessName, _ := pairSuccess.GetChildByTag("biz").Attrs["name"].(string)
	jid, _ := pairSuccess.GetChildByTag("device").Attrs["jid"].(types.JID)
	lid, _ := pairSuccess.GetChildByTag("device").Attrs["lid"].(types.JID)
	platform, _ := pairSuccess.GetChildByTag("platform").Attrs["name"].(string)

	go func() {
		err := cli.handlePair(context.TODO(), deviceIdentityBytes, id, businessName, platform, jid, lid)
		if err != nil {
			cli.Log.Errorf("Failed to pair device: %v", err)
			cli.Disconnect()
			cli.dispatchEvent(&events.PairError{ID: jid, LID: lid, BusinessName: businessName, Platform: platform, Error: err})
		} else {
			cli.Log.Infof("Successfully paired %s", cli.Store.ID)
			cli.dispatchEvent(&events.PairSuccess{ID: jid, LID: lid, BusinessName: businessName, Platform: platform})
		}
	}()
}

func (cli *Client) handlePair(ctx context.Context, deviceIdentityBytes []byte, reqID, businessName, platform string, jid, lid types.JID) error {
	var deviceIdentityContainer waAdv.ADVSignedDeviceIdentityHMAC
	err := proto.Unmarshal(deviceIdentityBytes, &deviceIdentityContainer)
	if err != nil {
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairProtoError{"failed to parse device identity container in pair success message", err}
	}
	isHostedAccount := deviceIdentityContainer.AccountType != nil && *deviceIdentityContainer.AccountType == waAdv.ADVEncryptionType_HOSTED

	h := hmac.New(sha256.New, cli.Store.AdvSecretKey)
	if isHostedAccount {
		h.Write(AdvHostedPrefixDeviceIdentityAccountSignature)
	}
	h.Write(deviceIdentityContainer.Details)

	if !bytes.Equal(h.Sum(nil), deviceIdentityContainer.HMAC) {
		cli.Log.Warnf("Invalid HMAC from pair success message")
		cli.sendPairError(reqID, 401, "hmac-mismatch")
		return ErrPairInvalidDeviceIdentityHMAC
	}

	var deviceIdentity waAdv.ADVSignedDeviceIdentity
	err = proto.Unmarshal(deviceIdentityContainer.Details, &deviceIdentity)
	if err != nil {
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairProtoError{"failed to parse signed device identity in pair success message", err}
	}

	if !verifyDeviceIdentityAccountSignature(&deviceIdentity, cli.Store.IdentityKey, isHostedAccount) {
		cli.sendPairError(reqID, 401, "signature-mismatch")
		return ErrPairInvalidDeviceSignature
	}

	deviceIdentity.DeviceSignature = generateDeviceSignature(&deviceIdentity, cli.Store.IdentityKey, isHostedAccount)[:]

	var deviceIdentityDetails waAdv.ADVDeviceIdentity
	err = proto.Unmarshal(deviceIdentity.Details, &deviceIdentityDetails)
	if err != nil {
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairProtoError{"failed to parse device identity details in pair success message", err}
	}

	if cli.PrePairCallback != nil && !cli.PrePairCallback(jid, platform, businessName) {
		cli.sendPairError(reqID, 500, "internal-error")
		return ErrPairRejectedLocally
	}

	cli.Store.Account = proto.Clone(&deviceIdentity).(*waAdv.ADVSignedDeviceIdentity)

	mainDeviceLID := lid
	mainDeviceLID.Device = 0
	mainDeviceIdentity := *(*[32]byte)(deviceIdentity.AccountSignatureKey)
	deviceIdentity.AccountSignatureKey = nil

	selfSignedDeviceIdentity, err := proto.Marshal(&deviceIdentity)
	if err != nil {
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairProtoError{"failed to marshal self-signed device identity", err}
	}

	cli.Store.ID = &jid
	cli.Store.LID = lid
	cli.Store.BusinessName = businessName
	cli.Store.Platform = platform
	err = cli.Store.Save(ctx)
	if err != nil {
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairDatabaseError{"failed to save device store", err}
	}
	cli.StoreLIDPNMapping(ctx, lid, jid)
	err = cli.Store.Identities.PutIdentity(ctx, mainDeviceLID.SignalAddress().String(), mainDeviceIdentity)
	if err != nil {
		_ = cli.Store.Delete(ctx)
		cli.sendPairError(reqID, 500, "internal-error")
		return &PairDatabaseError{"failed to store main device identity", err}
	}

	// Expect a disconnect after this and don't dispatch the usual Disconnected event
	cli.expectDisconnect()

	err = cli.sendNode(waBinary.Node{
		Tag: "iq",
		Attrs: waBinary.Attrs{
			"to":   types.ServerJID,
			"type": "result",
			"id":   reqID,
		},
		Content: []waBinary.Node{{
			Tag: "pair-device-sign",
			Content: []waBinary.Node{{
				Tag: "device-identity",
				Attrs: waBinary.Attrs{
					"key-index": deviceIdentityDetails.GetKeyIndex(),
				},
				Content: selfSignedDeviceIdentity,
			}},
		}},
	})
	if err != nil {
		_ = cli.Store.Delete(ctx)
		return fmt.Errorf("failed to send pairing confirmation: %w", err)
	}
	return nil
}

func concatBytes(data ...[]byte) []byte {
	length := 0
	for _, item := range data {
		length += len(item)
	}
	output := make([]byte, length)
	ptr := 0
	for _, item := range data {
		ptr += copy(output[ptr:ptr+len(item)], item)
	}
	return output
}

func verifyDeviceIdentityAccountSignature(deviceIdentity *waAdv.ADVSignedDeviceIdentity, ikp *keys.KeyPair, isHostedAccount bool) bool {
	if len(deviceIdentity.AccountSignatureKey) != 32 || len(deviceIdentity.AccountSignature) != 64 {
		return false
	}

	signatureKey := ecc.NewDjbECPublicKey(*(*[32]byte)(deviceIdentity.AccountSignatureKey))
	signature := *(*[64]byte)(deviceIdentity.AccountSignature)

	prefix := AdvPrefixAccountSignature
	if isHostedAccount {
		prefix = AdvHostedPrefixDeviceIdentityAccountSignature
	}
	message := concatBytes(prefix, deviceIdentity.Details, ikp.Pub[:])
	return ecc.VerifySignature(signatureKey, message, signature)
}

func generateDeviceSignature(deviceIdentity *waAdv.ADVSignedDeviceIdentity, ikp *keys.KeyPair, isHostedAccount bool) *[64]byte {
	prefix := AdvPrefixDeviceSignatureGenerate
	if isHostedAccount {
		prefix = AdvHostedPrefixDeviceIdentityDeviceSignatureVerification
	}
	message := concatBytes(prefix, deviceIdentity.Details, ikp.Pub[:], deviceIdentity.AccountSignatureKey)
	sig := ecc.CalculateSignature(ecc.NewDjbECPrivateKey(*ikp.Priv), message)
	return &sig
}

func (cli *Client) sendPairError(id string, code int, text string) {
	err := cli.sendNode(waBinary.Node{
		Tag: "iq",
		Attrs: waBinary.Attrs{
			"to":   types.ServerJID,
			"type": "error",
			"id":   id,
		},
		Content: []waBinary.Node{{
			Tag: "error",
			Attrs: waBinary.Attrs{
				"code": code,
				"text": text,
			},
		}},
	})
	if err != nil {
		cli.Log.Errorf("Failed to send pair error node: %v", err)
	}
}
