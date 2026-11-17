package sdk_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/sdk"
)

// Every sample event reads into the SDK's types with nothing left over, so the
// types and the contract cannot drift apart unnoticed.
func TestSamplesMatchTypes(t *testing.T) {
	types := map[string]any{
		sdk.EventLoanCreated:     &sdk.LoanCreated{},
		sdk.EventPaymentReceived: &sdk.PaymentReceived{},
		sdk.EventLoanRedeemed:    &sdk.LoanRedeemed{},
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
			sdk.Envelope
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
	raw, _ := contract.Fixture(sdk.EventPaymentReceived)
	in := sdk.Input{Event: raw}
	p, err := in.PaymentReceived()
	if err != nil || p.Amount.Paise != 1035000 || p.Payment.Number != "H-0312" || p.Customer.PhoneE164 != "+919876543210" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := in.LoanCreated(); err == nil {
		t.Error("a payment read as a loan")
	}
	if in.EventType() != sdk.EventPaymentReceived {
		t.Errorf("event type %q", in.EventType())
	}
}
