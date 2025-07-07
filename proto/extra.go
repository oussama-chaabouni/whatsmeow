package armadillo

import (
	"google.golang.org/protobuf/proto"

	"github.com/oussama-chaabouni/whatsmeow/proto/instamadilloAddMessage"
	"github.com/oussama-chaabouni/whatsmeow/proto/instamadilloDeleteMessage"
	"github.com/oussama-chaabouni/whatsmeow/proto/instamadilloSupplementMessage"
	"github.com/oussama-chaabouni/whatsmeow/proto/waArmadilloApplication"
	"github.com/oussama-chaabouni/whatsmeow/proto/waCommon"
	"github.com/oussama-chaabouni/whatsmeow/proto/waConsumerApplication"
	"github.com/oussama-chaabouni/whatsmeow/proto/waMultiDevice"
)

type MessageApplicationSub interface {
	IsMessageApplicationSub()
}

type RealMessageApplicationSub interface {
	MessageApplicationSub
	proto.Message
}

type Unsupported_BusinessApplication waCommon.SubProtocol
type Unsupported_PaymentApplication waCommon.SubProtocol
type Unsupported_Voip waCommon.SubProtocol

var (
	_ MessageApplicationSub = (*waConsumerApplication.ConsumerApplication)(nil) // 2
	_ MessageApplicationSub = (*Unsupported_BusinessApplication)(nil)           // 3
	_ MessageApplicationSub = (*Unsupported_PaymentApplication)(nil)            // 4
	_ MessageApplicationSub = (*waMultiDevice.MultiDevice)(nil)                 // 5
	_ MessageApplicationSub = (*Unsupported_Voip)(nil)                          // 6
	_ MessageApplicationSub = (*waArmadilloApplication.Armadillo)(nil)          // 7

	_ MessageApplicationSub = (*instamadilloAddMessage.AddMessagePayload)(nil)
	_ MessageApplicationSub = (*instamadilloSupplementMessage.SupplementMessagePayload)(nil)
	_ MessageApplicationSub = (*instamadilloDeleteMessage.DeleteMessagePayload)(nil)
)

func (*Unsupported_BusinessApplication) IsMessageApplicationSub() {}
func (*Unsupported_PaymentApplication) IsMessageApplicationSub()  {}
func (*Unsupported_Voip) IsMessageApplicationSub()                {}
