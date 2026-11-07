# WhatsApp Cloud API

Sends the messages Girvi composes (loan created, payment received, loan closed) to
customers as approved WhatsApp templates, from your own WhatsApp Business number.

Each firm uses its own number and token. Nothing is sent until you turn the
extension on for a firm in Girvi.

## Set it up

These steps follow Meta's own
[Get Started guide](https://developers.facebook.com/documentation/business-messaging/whatsapp/get-started).
You do them once per business number.

**On Meta's side**

1. In the [Meta App Dashboard](https://developers.facebook.com/apps), create an app and choose
   **Connect with customers through WhatsApp**.
2. Open **WhatsApp, API Setup**. Connect your Messaging account (create one if you do not have it) and
   add your phone number. Note two IDs on that page:
   - **Phone number ID**
   - **WhatsApp Business Account ID** (Girvi calls it the Messaging account ID)
3. In [Business Settings](https://business.facebook.com/latest/settings), open **System users** and add one.
   Assign it your app (**Manage app**, full control) and your WhatsApp account (**Manage WhatsApp Business
   accounts**, full control). Then **Generate token** with these permissions:
   `business_management`, `whatsapp_business_messaging`, `whatsapp_business_management`.
   Copy the token. The temporary token on the API Setup page stops working after a day, so do not use it.

**In Girvi**

4. Open **Extensions**, approve **WhatsApp Cloud API**, and open its **Settings**. Enter the three values
   above. Leave "Set up message templates for me" on.
5. Turn the extension on for your firm.

Girvi then creates three message templates in your account (loan created, payment received, loan closed) in
the Utility category and sends them to Meta for approval. The extension's status shows how many are approved.
Until a template is approved, its messages wait, and they are sent once it is.
The `whatsapp_business_management` permission is what lets Girvi create them.

Business-initiated messages to a customer who has not written to you in the last 24 hours can only be
approved templates. Meta charges per template message, so check its pricing page.

## Using your own templates

Turn off "Set up message templates for me", create your templates in WhatsApp Manager, and under
**Advanced** enter each template's name, language and the Girvi values that fill `{{1}}`, `{{2}}` and so on.

Variables Girvi provides:

| Message | Variables |
|---|---|
| Loan created | `customer_name`, `loan_number`, `amount`, `weight`, `interest_rate`, `pledged_on` |
| Payment received | `customer_name`, `loan_number`, `amount_paid`, `balance_due` |
| Loan closed | `customer_name`, `loan_number`, `weight` |

## Trying it

Meta gives a new number a template called `hello_world` and lets you send it to up to five verified numbers
for free. **Send test** in Girvi uses it.

## Not covered yet

- Delivered and read receipts. Meta reports them by webhook, which a desktop app cannot receive. A message
  shows as **Sent** once WhatsApp has accepted it, never as delivered. Meta can still fail it afterwards, for
  example when the business account is locked, and Girvi will not know. Check Business Support Home in Meta
  Business Suite if messages are not arriving.
- Whether each customer has agreed to receive WhatsApp messages. Meta's policy requires it, and keeping that
  consent is the shop's responsibility.
- Creating templates through the API has been tested against a stand-in for Meta, not against a real account.
  If Meta refuses a template, the extension's status says why, and you can create it by hand instead.
