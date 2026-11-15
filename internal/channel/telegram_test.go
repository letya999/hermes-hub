package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

type keyboardReceiptSender struct {
	captureTelegramSender
	id  int
	err error
}

func (s *keyboardReceiptSender) SendKeyboardReceipt(context.Context, int64, int64, string, []commodel.DeliveryButton) (int, error) {
	return s.id, s.err
}

func TestTelegramPersistsHumanConfirmationReceipt(t *testing.T) {
	failure := errors.New("receipt unavailable")
	for _, tc := range []struct {
		name              string
		id                int
		sendErr, storeErr error
		persisted         bool
	}{
		{"acknowledged", 47, nil, nil, true},
		{"send failed", 0, failure, nil, false},
		{"missing receipt", 0, nil, nil, false},
		{"persistence failed", 47, nil, failure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &keyboardReceiptSender{id: tc.id, err: tc.sendErr}
			var receipt commodel.Delivery
			ch := NewTelegram(TelegramDeps{API: func() TelegramSender { return sender }, VoicePlan: func(commodel.Delivery) (string, bool) { return "", false }, UpdateSending: func(d commodel.Delivery) error { receipt = d; return tc.storeErr }})
			d := commodel.Delivery{ID: "test-confirmation", Channel: "telegram_bot", ChatID: 11, Kind: "human_confirmation", Text: "test", Buttons: []commodel.DeliveryButton{{Text: "test", Data: "h:test"}}}
			err := ch.Deliver(t.Context(), &d)
			if (receipt.TelegramMessageID == 47) != tc.persisted || (err == nil) != (tc.name == "acknowledged") {
				t.Fatalf("persisted=%t err=%v", receipt.TelegramMessageID == 47, err)
			}
		})
	}
}

// A blocked notice must reach the owner's CURRENT verified home, not the
// schedule's durable envelope target: a stale binding (chat remapped to
// another principal since enrollment) is exactly what may have caused the
// block — following it reroutes the notice to the wrong principal.
func TestRouteBlockedNoticeUsesCurrentOwnerBinding(t *testing.T) {
	ch := NewTelegram(TelegramDeps{
		UserByPrincipal: func(id string) commodel.User {
			if id != "local" {
				t.Fatalf("unexpected principal %q", id)
			}
			return commodel.User{ID: "local", TelegramIDs: []int64{111}}
		},
	})
	item := commodel.Schedule{Channel: "telegram_bot", Envelope: identity.Envelope{PrincipalID: "local", DeliveryTargetID: "telegram-480637186"}}
	d := commodel.Delivery{ChatID: 480637186, ConversationID: "telegram-480637186", DeliveryTargetID: "telegram-480637186"}
	ch.RouteBlockedNotice(item, &d)
	if d.ChatID != 111 || d.DeliveryTargetID != "telegram-111" {
		t.Fatalf("notice must follow the current owner binding: %+v", d)
	}
}

// An owner with no current telegram binding has no verified home: the notice
// must clear the address (normalize then refuses it) rather than panic or
// follow the stale envelope target.
func TestRouteBlockedNoticeNoBindingClearsAddress(t *testing.T) {
	ch := NewTelegram(TelegramDeps{
		UserByPrincipal: func(id string) commodel.User { return commodel.User{ID: id} },
	})
	item := commodel.Schedule{Channel: "telegram_bot", Envelope: identity.Envelope{PrincipalID: "local", DeliveryTargetID: "telegram-480637186"}}
	d := commodel.Delivery{ChatID: 480637186, ConversationID: "telegram-480637186", DeliveryTargetID: "telegram-480637186"}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("unbound owner must not panic: %v", r)
			}
		}()
		ch.RouteBlockedNotice(item, &d)
	}()
	if err := ch.NormalizeDelivery(&d); err == nil {
		t.Fatalf("notice to an unbound owner must not normalize: %+v", d)
	}
}
