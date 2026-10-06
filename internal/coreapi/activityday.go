package coreapi

import "encoding/json"

// ActivityDay is an optional, nullable UTC day from the object directory.
// Present distinguishes an explicit null (no recorded activity) from an absent
// field, which permits the legacy lastClientAt fallback. The day is a lifetime
// dormancy signal, not an exact timestamp from retained client history.
type ActivityDay struct {
	Present bool
	Day     *string
}

func (d *ActivityDay) UnmarshalJSON(data []byte) error {
	var day *string
	if err := json.Unmarshal(data, &day); err != nil {
		return err
	}
	*d = ActivityDay{Present: true, Day: day}
	return nil
}

// IsZero lets the enclosing response omit a field absent from the wire.
func (d ActivityDay) IsZero() bool { return !d.Present }

func (d ActivityDay) MarshalJSON() ([]byte, error) { return json.Marshal(d.Day) }
