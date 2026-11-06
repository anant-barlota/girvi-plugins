// Command whatsapp is the official WhatsApp Cloud API extension for Girvi. It
// sends the messages the host composes (pledge created, payment received, loan
// closed) as approved WhatsApp templates, from each firm's own business number.
package main

import "github.com/anant-barlota/girvi-plugins/sdk"

func main() {
	sdk.Run(newWhatsApp())
}
