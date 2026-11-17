package sdk

// The events a plugin can subscribe to, and their data. This is the girvi/1
// contract: adding a field is compatible; removing or renaming one is not.
//
// Money is integer paise and weight is integer milligrams, each with a
// display string Girvi formats the way its printed documents do, so a plugin
// never formats or computes money. Dates are ISO 8601.
const (
	EventLoanCreated     = "loan.created"
	EventPaymentReceived = "payment.received"
	EventLoanRedeemed    = "loan.redeemed"
)

type Money struct {
	Paise   int64  `json:"paise"` // 150000 is ₹1,500.00
	Display string `json:"display"`
}

type Weight struct {
	Milligrams int64  `json:"milligrams"`
	Display    string `json:"display"` // "10.500 g"
}

type Date struct {
	ISO     string `json:"iso"`     // "2026-11-12"
	Display string `json:"display"` // "12 Nov 2026"
}

type Rate struct {
	BasisPoints int64  `json:"basis_points"` // hundredths of a percent: 1400 is 14%
	Per         string `json:"per"`          // "year"
	Display     string `json:"display"`      // "14% p.a."
}

// Customer is who an event is about. Name and the phone fields are present
// only if plugin.json lists customer.name and customer.phone in "fields".
type Customer struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Phone     string `json:"phone,omitempty"`      // as the record holds it
	PhoneE164 string `json:"phone_e164,omitempty"` // "+919876543210", only for a usable Indian mobile number
}

type LoanRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// PaymentRef is the Form H a payment was recorded on.
type PaymentRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

type Loan struct {
	ID               string `json:"id"`
	Number           string `json:"number"`
	PledgedOn        Date   `json:"pledged_on"`
	Principal        Money  `json:"principal"`
	Value            Money  `json:"value"`
	InterestRate     *Rate  `json:"interest_rate,omitempty"` // absent when the loan has no rate
	TotalNetWeight   Weight `json:"total_net_weight"`
	TotalGrossWeight Weight `json:"total_gross_weight"`
	TotalPieces      int64  `json:"total_pieces"`
	Status           string `json:"status"` // active | partially_paid | redeemed | cancelled | auctioned
}

// LoanCreated is sent when a Form F is saved.
type LoanCreated struct {
	Customer Customer `json:"customer"`
	Loan     Loan     `json:"loan"`
}

// PaymentReceived is sent for every Form H, including the one that closes the
// loan; then LoanClosed is true and a loan.redeemed follows.
type PaymentReceived struct {
	Customer             Customer   `json:"customer"`
	Loan                 LoanRef    `json:"loan"`
	Payment              PaymentRef `json:"payment"`
	Amount               Money      `json:"amount"` // principal plus interest
	Principal            Money      `json:"principal"`
	Interest             Money      `json:"interest"`
	Date                 Date       `json:"date"`
	OutstandingPrincipal Money      `json:"outstanding_principal"`
	LoanClosed           bool       `json:"loan_closed"`
}

// LoanRedeemed is sent after the closing payment.received, when a loan is paid
// off. A plugin that subscribes to both should act on one of them.
type LoanRedeemed struct {
	Customer       Customer `json:"customer"`
	Loan           LoanRef  `json:"loan"`
	RedeemedOn     Date     `json:"redeemed_on"`
	ReturnedWeight Weight   `json:"returned_weight"`
}
