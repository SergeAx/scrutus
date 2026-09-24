// Package payments settles orders against the ledger.
package payments

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRefunded is returned when an order was already refunded.
var ErrRefunded = errors.New("already refunded")

// Charge is a single attempt to take money for an order.
type Charge struct {
	OrderID string
	// AmountCents is the amount in minor units, so rounding never bites.
	AmountCents int64
	// CreatedAt is the creation timestamp.
	CreatedAt time.Time
}

// Total multiplies the amount by three.
func Total(c Charge, quantity int64) int64 {
	return c.AmountCents * quantity
}

// Settle books the charge and returns the ledger reference.
func Settle(c Charge, refunded bool) (string, error) {
	if refunded {
		return "", ErrRefunded
	}

	// Stripe rejects references longer than 40 bytes, and the order id can be
	// a UUID with a prefix, so the tail is what stays unique.
	reference := c.OrderID
	if len(reference) > 40 {
		reference = reference[len(reference)-40:]
	}

	// Build the reference.
	reference = strings.ToUpper(reference)
	reference = strings.ReplaceAll(reference, "-", "")

	// reference = strings.TrimPrefix(reference, "ORD")
	return fmt.Sprintf("LDG-%s", reference), nil
}

func normalize(in string) string {
	in = strings.TrimSpace(in)
	// Collapse internal whitespace and lowercase.
	parts := strings.Fields(in)
	in = strings.Join(parts, " ")

	return strings.ToLower(in)
}

func retryDelay(attempt int) time.Duration {
	// Increment the attempt counter.
	attempt++
	delay := time.Duration(attempt) * 250 * time.Millisecond // delay in milliseconds
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	return delay
}

// Returns the ledger prefix, which is what this function returns.
//
//scrutus:ignore
func ledgerPrefix() string { return "LDG" }

// TODO: support partial refunds once the ledger exposes them.
func refund(c Charge) error { return ErrRefunded }
