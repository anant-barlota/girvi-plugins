package plugin_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/anant-barlota/girvi-sdk/plugin"
	"github.com/anant-barlota/girvi-sdk/plugin/contract"
	"github.com/anant-barlota/girvi-sdk/plugin/plugintest"
)

// Every sample event reads into the SDK's types with nothing left over, so the
// types and the contract cannot drift apart unnoticed.
func TestSamplesMatchTypes(t *testing.T) {
	types := map[string]any{
		plugin.EventLoanCreated:     &plugin.LoanCreated{},
		plugin.EventPaymentReceived: &plugin.PaymentReceived{},
		plugin.EventLoanRedeemed:    &plugin.LoanRedeemed{},
	}
	if got := contract.Events(); len(got) != len(types) {
		t.Fatalf("samples %v, types for %d", got, len(types))
	}
	for event, into := range types {
		raw, err := contract.Fixture(event)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			plugin.Envelope
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.Type != event || env.ID == "" {
			t.Fatalf("%s envelope: %+v %v", event, env.Envelope, err)
		}
		dec := json.NewDecoder(bytes.NewReader(env.Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			t.Errorf("%s: %v", event, err)
		}
	}
}

func TestTypedReads(t *testing.T) {
	raw, _ := contract.Fixture(plugin.EventPaymentReceived)
	in := plugin.Input{Event: raw}
	p, err := in.PaymentReceived()
	if err != nil || p.Amount.Paise != 1035000 || p.Payment.Number != "H-0312" || p.Customer.PhoneE164 != "+919876543210" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := in.LoanCreated(); err == nil {
		t.Error("a payment read as a loan")
	}
	if in.EventType() != plugin.EventPaymentReceived {
		t.Errorf("event type %q", in.EventType())
	}
}

// A scheduled call carries the payload and the firm's settings, and no event.
func TestWakeInput(t *testing.T) {
	in := plugintest.WakeInput("loan-1", map[string]string{"url": "https://example.com"})
	if in.Kind != plugin.KindWake || in.Wake != "loan-1" || in.EventType() != "" || in.Settings["url"] == "" || in.Firm.ID != "bk-moolchand" {
		t.Fatalf("%+v", in)
	}
	if plugintest.WakeInput("x", nil).Settings == nil {
		t.Error("nil settings must become an empty map, as Girvi gives")
	}
}
