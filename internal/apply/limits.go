package apply

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/overad9658/tagherd/internal/model"
)

// Provider limits on user tags.
const (
	awsMaxTags        = 50
	awsMaxKey         = 128
	awsMaxValue       = 256
	azureMaxTags      = 50
	azureMaxKey       = 512
	azureMaxValue     = 256
	gcpMaxLabels      = 64
	gcpMaxRMValue     = 63
	awsReservedPrefix = "aws:"
)

var (
	labelKey   = regexp.MustCompile(`^[\p{Ll}\p{Lo}][\p{Ll}\p{Lo}\p{N}_-]{0,62}$`)
	labelValue = regexp.MustCompile(`^[\p{Ll}\p{Lo}\p{N}_-]{0,63}$`)
)

// checkLimits checks one resource's tags after the writes are merged in.
// A resource that would break a limit is not written at all.
func checkLimits(cloud model.Cloud, existing, writes []model.Tag) error {
	switch cloud {
	case model.AWS:
		keys := map[string]bool{}
		for _, t := range existing {
			if !reserved(t.Key) {
				keys[t.Key] = true
			}
		}
		for _, t := range writes {
			switch {
			case reserved(t.Key):
				return fmt.Errorf("limit: %s uses the reserved aws: prefix", t.Key)
			case utf8.RuneCountInString(t.Key) > awsMaxKey:
				return fmt.Errorf("limit: key %s is longer than %d characters", t.Key, awsMaxKey)
			case utf8.RuneCountInString(t.Value) > awsMaxValue:
				return fmt.Errorf("limit: %s value is longer than %d characters", t.Key, awsMaxValue)
			}
			keys[t.Key] = true
		}
		if len(keys) > awsMaxTags {
			return fmt.Errorf("limit: the resource would have %d tags, more than %d", len(keys), awsMaxTags)
		}
	case model.Azure:
		keys := map[string]bool{}
		for _, t := range existing {
			keys[strings.ToLower(t.Key)] = true
		}
		for _, t := range writes {
			switch {
			case utf8.RuneCountInString(t.Key) > azureMaxKey:
				return fmt.Errorf("limit: tag name %s is longer than %d characters", t.Key, azureMaxKey)
			case utf8.RuneCountInString(t.Value) > azureMaxValue:
				return fmt.Errorf("limit: %s value is longer than %d characters", t.Key, azureMaxValue)
			}
			keys[strings.ToLower(t.Key)] = true
		}
		if len(keys) > azureMaxTags {
			return fmt.Errorf("limit: the resource would have %d tags, more than %d", len(keys), azureMaxTags)
		}
	case model.GCP:
		labels := map[string]bool{}
		for _, t := range existing {
			if t.Kind == model.KindLabel {
				labels[t.Key] = true
			}
		}
		for _, t := range writes {
			if t.Kind == model.KindRMTag {
				if t.Key == "" || utf8.RuneCountInString(t.Value) > gcpMaxRMValue {
					return fmt.Errorf("limit: Resource Manager tag %s value must be 1 to %d characters", t.Key, gcpMaxRMValue)
				}
				continue
			}
			switch {
			case !labelKey.MatchString(t.Key):
				return fmt.Errorf("limit: %q is not a valid label key", t.Key)
			case !labelValue.MatchString(t.Value):
				return fmt.Errorf("limit: %q is not a valid value for label %s", t.Value, t.Key)
			}
			labels[t.Key] = true
		}
		if len(labels) > gcpMaxLabels {
			return fmt.Errorf("limit: the resource would have %d labels, more than %d", len(labels), gcpMaxLabels)
		}
	}
	return nil
}

func reserved(key string) bool {
	return len(key) >= len(awsReservedPrefix) && strings.EqualFold(key[:len(awsReservedPrefix)], awsReservedPrefix)
}
