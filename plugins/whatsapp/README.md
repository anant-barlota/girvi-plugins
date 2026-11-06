# WhatsApp Cloud API

Sends the messages Girvi composes (loan created, payment received, loan closed) to
customers as approved WhatsApp templates, from your own WhatsApp Business number.

Each firm uses its own number and token. Nothing is sent until you turn the
extension on for a firm in Girvi.

## What you set up on Meta's side

1. A WhatsApp Business account and a phone number in Meta Business Suite.
2. A permanent access token for a system user that can send WhatsApp messages.
3. One message template per message you want to send, in the **Utility**
   category, and wait for Meta to approve it. Business-initiated messages to a
   customer who has not written to you in the last 24 hours can only be
   templates. Meta charges per template message; check its pricing page.

## What you enter in Girvi

| Setting | Where to find it |
|---|---|
| Phone number ID | WhatsApp Manager, the number's details. It is an ID, not the phone number. |
| Access token | The system user's token. |
| Template name and language | The name and language you gave the template in Meta. |
| Template variables | The Girvi variables that fill `{{1}}`, `{{2}}` and so on, in order. |

While a setting is missing or Meta rejects the token or the template, messages
wait and the extension shows as blocked. They are sent once it is fixed, unless
they have expired (24 hours).

## Variables

| Message | Variables Girvi provides |
|---|---|
| Loan created | `customer_name`, `loan_number`, `amount`, `weight`, `interest_rate`, `pledged_on` |
| Payment received | `customer_name`, `loan_number`, `amount_paid`, `balance_due` |
| Loan closed | `customer_name`, `loan_number`, `weight` |

Example template body for a loan created message, using the default order:

> Hello {{1}}, your loan {{2}} for {{3}} against {{4}} at {{5}} was recorded on {{6}}.

## Trying it

Meta gives every new number a template called `hello_world` and lets you send it
to up to five verified numbers for free. The Send test button in Girvi uses it.

## Not covered yet

- Delivered and read receipts. Meta reports them by webhook, which a desktop app
  cannot receive. A message shows as delivered once WhatsApp has accepted it.
- Whether each customer has agreed to receive WhatsApp messages. Meta's policy
  requires it, and keeping that consent is the shop's responsibility.
