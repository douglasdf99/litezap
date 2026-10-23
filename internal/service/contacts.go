package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// Address book (CON-2): saving and removing contacts of the account through the app
// state. The phone and the linked devices follow, and with save_on_phone the contact is
// also written to the phone's own address book.

// Limits of address book changes, per instance (each one syncs to the phone).
const (
	contactChangesPerMinute = 10
	contactChangesPerDay    = 200
	maxContactNameRunes     = 64
)

// AddressBookInput is the body of POST /contacts.
type AddressBookInput struct {
	Phone     string `json:"phone"` // with country code, or a phone-number JID
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	// SaveOnPhone also saves the contact in the phone's own address book (default true,
	// as WhatsApp's "sync contact to phone").
	SaveOnPhone *bool `json:"save_on_phone,omitempty"`
}

// SavedContact is a contact saved in the account's address book.
type SavedContact struct {
	JID         string `json:"jid"`
	LID         string `json:"lid,omitempty"`
	FirstName   string `json:"first_name"`
	FullName    string `json:"full_name"`
	SaveOnPhone bool   `json:"save_on_phone"`
}

func (a *Account) takeContactChange(instanceID string) error {
	if !a.contactQuota.take(instanceID, time.Now(), contactChangesPerMinute, contactChangesPerDay) {
		return apierr.New(http.StatusTooManyRequests, "rate_limited",
			"too many address book changes for this instance (10 per minute, 200 per day)", true)
	}
	return nil
}

// contactJID resolves the phone of a contact to its WhatsApp JID: a chat of the log, or
// a number lookup (which spends the check-numbers quota) for numbers never seen.
func (a *Account) contactJID(ctx context.Context, instanceID, phone string) (string, error) {
	jid, err := ChatJID(phone)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(jid, "@s.whatsapp.net") {
		return "", apierr.Validation("phone must be a phone number (or a …@s.whatsapp.net JID)")
	}
	if strings.Contains(phone, "@") {
		return jid, nil
	}
	if _, err := a.st().GetChat(ctx, instanceID, jid); err == nil {
		return jid, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	res, err := a.Messaging.CheckNumbers(ctx, instanceID, []string{phone})
	if err != nil {
		return "", err
	}
	if len(res) == 0 || !res[0].Exists || res[0].JID == "" {
		return "", apierr.New(http.StatusUnprocessableEntity, "invalid_number", "this number is not on WhatsApp", false)
	}
	return res[0].JID, nil
}

// SaveContact saves (or renames) a contact in the account's address book.
func (a *Account) SaveContact(ctx context.Context, instanceID string, in AddressBookInput) (SavedContact, error) {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return SavedContact{}, mapErr(err)
	}
	first, last := strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName)
	switch {
	case strings.TrimSpace(in.Phone) == "":
		return SavedContact{}, apierr.Validation("phone is required")
	case first == "":
		return SavedContact{}, apierr.Validation("first_name is required")
	case utf8.RuneCountInString(first) > maxContactNameRunes || utf8.RuneCountInString(last) > maxContactNameRunes:
		return SavedContact{}, apierr.Validation("first_name and last_name are limited to 64 characters")
	case strings.ContainsAny(first+last, "\r\n\t"):
		return SavedContact{}, apierr.Validation("names cannot contain line breaks or tabs")
	}
	saveOnPhone := in.SaveOnPhone == nil || *in.SaveOnPhone
	jid, err := a.contactJID(ctx, instanceID, in.Phone)
	if err != nil {
		return SavedContact{}, err
	}
	if err := a.takeContactChange(instanceID); err != nil {
		return SavedContact{}, err
	}
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return SavedContact{}, err
	}
	full := strings.TrimSpace(first + " " + last)
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.ContactAction(rctx, &workerv1.ContactActionRequest{Jid: jid, Action: "save", FirstName: first,
		FullName: full, SaveOnPhone: saveOnPhone})
	if err != nil {
		return SavedContact{}, accountErr(err)
	}
	return SavedContact{JID: resp.Jid, LID: resp.Lid, FirstName: first, FullName: full, SaveOnPhone: saveOnPhone}, nil
}

// RemoveContact removes a contact from the account's address book (the chat and its
// messages stay; WhatsApp then shows the number or the contact's own name).
func (a *Account) RemoveContact(ctx context.Context, instanceID, raw string) error {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return mapErr(err)
	}
	jid, err := ChatJID(raw)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(jid, "@s.whatsapp.net") {
		return apierr.Validation("contacts are removed by phone number or …@s.whatsapp.net JID")
	}
	if err := a.takeContactChange(instanceID); err != nil {
		return err
	}
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.ContactAction(rctx, &workerv1.ContactActionRequest{Jid: jid, Action: "remove"}); err != nil {
		return accountErr(err)
	}
	return nil
}
