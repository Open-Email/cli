package cli

import "github.com/Open-Email/cli/internal/compose"

// newDeliveryID mints a fresh ULID X-Delivery-Id. The implementation lives in
// internal/compose (shared with the console); this forwarder keeps the many
// cli call sites unchanged.
func newDeliveryID() string { return compose.NewDeliveryID() }
