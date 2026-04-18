package headscale

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestampFromTime wraps timestamppb.New — exists only so the rest of
// the package can import "time" but not the protobuf-specific type.
func timestampFromTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}
