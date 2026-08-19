package smartrouter

import (
	"errors"
	"fmt"
	"strings"
)

const (
	MediaKindImage = "image"
	MediaKindVideo = "video"
	MediaKindAudio = "audio"
)

// MediaReferenceShape contains only metadata needed to select a compatible
// physical route. It never carries a URL, file ID, body, or prompt.
type MediaReferenceShape struct {
	Type            string  `json:"type"`
	Role            string  `json:"role,omitempty"`
	SizeBytes       int64   `json:"size_bytes,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	MetadataTrusted bool    `json:"metadata_trusted,omitempty"`
}

// MediaRequestShape is request-scoped planning metadata. Prices derived from
// it are ordering hints only and never participate in billing or settlement.
type MediaRequestShape struct {
	Kind                   string                `json:"kind"`
	DurationSeconds        int64                 `json:"duration_seconds,omitempty"`
	DurationKnown          bool                  `json:"duration_known,omitempty"`
	OutputCount            int64                 `json:"output_count,omitempty"`
	OutputCountKnown       bool                  `json:"output_count_known,omitempty"`
	Resolution             string                `json:"resolution,omitempty"`
	Quality                string                `json:"quality,omitempty"`
	AspectRatio            string                `json:"aspect_ratio,omitempty"`
	References             []MediaReferenceShape `json:"references,omitempty"`
	EstimatedTokens        int64                 `json:"estimated_tokens,omitempty"`
	EstimatedTokensKnown   bool                  `json:"estimated_tokens_known,omitempty"`
	QuotaPerUnit           int64                 `json:"quota_per_unit,omitempty"`
	RequestPriceMultiplier int64                 `json:"request_price_multiplier_ppm,omitempty"`
}

func (shape MediaRequestShape) Validate() error {
	switch shape.Kind {
	case MediaKindImage, MediaKindVideo, MediaKindAudio:
	default:
		return fmt.Errorf("smartrouter: unsupported media kind %q", shape.Kind)
	}
	if shape.DurationKnown && shape.DurationSeconds <= 0 {
		return errors.New("smartrouter: known media duration must be positive")
	}
	if shape.OutputCountKnown && shape.OutputCount <= 0 {
		return errors.New("smartrouter: known media output count must be positive")
	}
	if shape.EstimatedTokensKnown && shape.EstimatedTokens <= 0 {
		return errors.New("smartrouter: known media token estimate must be positive")
	}
	if shape.QuotaPerUnit < 0 || shape.RequestPriceMultiplier < 0 {
		return errors.New("smartrouter: media price metadata cannot be negative")
	}
	for index, reference := range shape.References {
		switch normalizedMediaValue(reference.Type) {
		case MediaKindImage, MediaKindVideo, MediaKindAudio:
		default:
			return fmt.Errorf("smartrouter: reference %d has unsupported type %q", index, reference.Type)
		}
		if reference.SizeBytes < 0 || reference.DurationSeconds < 0 {
			return fmt.Errorf("smartrouter: reference %d has invalid metadata", index)
		}
	}
	return nil
}

type MediaDurationContract struct {
	Mode    string `json:"mode,omitempty"`
	Min     int    `json:"min,omitempty"`
	Max     int    `json:"max,omitempty"`
	Step    int    `json:"step,omitempty"`
	Default int    `json:"default,omitempty"`
	Fixed   int    `json:"fixed,omitempty"`
}

type MediaOutputCountContract struct {
	Known   bool `json:"known,omitempty"`
	Min     int  `json:"min,omitempty"`
	Max     int  `json:"max,omitempty"`
	Default int  `json:"default,omitempty"`
}

// MediaCapabilityContract is advisory route-local capability data. Unknown
// optional lists remain empty and therefore do not suppress a route.
type MediaCapabilityContract struct {
	ReferenceImage                bool                     `json:"reference_image,omitempty"`
	ReferenceVideo                bool                     `json:"reference_video,omitempty"`
	ReferenceAudio                bool                     `json:"reference_audio,omitempty"`
	ImageRoles                    []string                 `json:"image_roles,omitempty"`
	VideoRoles                    []string                 `json:"video_roles,omitempty"`
	AudioRoles                    []string                 `json:"audio_roles,omitempty"`
	MinImages                     int                      `json:"min_images,omitempty"`
	MaxImages                     int                      `json:"max_images,omitempty"`
	MaxVideos                     int                      `json:"max_videos,omitempty"`
	MaxAudios                     int                      `json:"max_audios,omitempty"`
	MaxTotalReferences            int                      `json:"max_total_references,omitempty"`
	MaxImageBytes                 int64                    `json:"max_image_bytes,omitempty"`
	MaxVideoBytes                 int64                    `json:"max_video_bytes,omitempty"`
	MaxAudioBytes                 int64                    `json:"max_audio_bytes,omitempty"`
	MinReferenceVideoSeconds      int                      `json:"min_reference_video_seconds,omitempty"`
	MaxReferenceVideoSeconds      int                      `json:"max_reference_video_seconds,omitempty"`
	MaxTotalReferenceVideoSeconds int                      `json:"max_total_reference_video_seconds,omitempty"`
	AspectRatios                  []string                 `json:"aspect_ratios,omitempty"`
	Resolutions                   []string                 `json:"resolutions,omitempty"`
	Qualities                     []string                 `json:"qualities,omitempty"`
	Duration                      MediaDurationContract    `json:"duration,omitempty"`
	OutputCount                   MediaOutputCountContract `json:"output_count,omitempty"`
	ProviderContract              string                   `json:"provider_contract,omitempty"`
	Revision                      string                   `json:"revision,omitempty"`
}

func (contract MediaCapabilityContract) Validate() error {
	for _, value := range []int{
		contract.MinImages, contract.MaxImages, contract.MaxVideos, contract.MaxAudios,
		contract.MaxTotalReferences, contract.MinReferenceVideoSeconds,
		contract.MaxReferenceVideoSeconds, contract.MaxTotalReferenceVideoSeconds,
		contract.Duration.Min, contract.Duration.Max, contract.Duration.Step,
		contract.Duration.Default, contract.Duration.Fixed, contract.OutputCount.Min,
		contract.OutputCount.Max, contract.OutputCount.Default,
	} {
		if value < 0 {
			return errors.New("smartrouter: media capability values cannot be negative")
		}
	}
	for _, value := range []int64{contract.MaxImageBytes, contract.MaxVideoBytes, contract.MaxAudioBytes} {
		if value < 0 {
			return errors.New("smartrouter: media capability byte limits cannot be negative")
		}
	}
	switch normalizedMediaValue(contract.Duration.Mode) {
	case "":
	case "fixed":
		if contract.Duration.Fixed <= 0 {
			return errors.New("smartrouter: fixed media duration must be positive")
		}
	case "range":
		if contract.Duration.Min <= 0 || contract.Duration.Max < contract.Duration.Min {
			return errors.New("smartrouter: media duration range is invalid")
		}
	default:
		return fmt.Errorf("smartrouter: unsupported media duration mode %q", contract.Duration.Mode)
	}
	if contract.OutputCount.Known {
		if contract.OutputCount.Min <= 0 || contract.OutputCount.Max < contract.OutputCount.Min {
			return errors.New("smartrouter: media output count range is invalid")
		}
		if contract.OutputCount.Default != 0 &&
			(contract.OutputCount.Default < contract.OutputCount.Min || contract.OutputCount.Default > contract.OutputCount.Max) {
			return errors.New("smartrouter: media output count default is outside the supported range")
		}
	}
	return nil
}

// MatchMediaRequest returns a precise route rejection without inventing a
// negative capability from an unspecified optional list.
func MatchMediaRequest(contract *MediaCapabilityContract, shape *MediaRequestShape) RejectReason {
	if contract == nil || shape == nil {
		return ""
	}
	counts := map[string]int{}
	totalVideoSeconds := 0.0
	for _, reference := range shape.References {
		referenceType := normalizedMediaValue(reference.Type)
		role := normalizedMediaValue(reference.Role)
		if role == "" {
			role = "reference"
		}
		counts[referenceType]++
		switch referenceType {
		case MediaKindImage:
			if !contract.ReferenceImage {
				return RejectMediaReferenceUnsupported
			}
			if !mediaValueAllowedWhenDeclared(role, contract.ImageRoles) {
				return RejectMediaReferenceRole
			}
			if reference.MetadataTrusted && contract.MaxImageBytes > 0 && reference.SizeBytes > contract.MaxImageBytes {
				return RejectMediaReferenceLimit
			}
		case MediaKindVideo:
			if !contract.ReferenceVideo {
				return RejectMediaReferenceUnsupported
			}
			if !mediaValueAllowedWhenDeclared(role, contract.VideoRoles) {
				return RejectMediaReferenceRole
			}
			if reference.MetadataTrusted && contract.MaxVideoBytes > 0 && reference.SizeBytes > contract.MaxVideoBytes {
				return RejectMediaReferenceLimit
			}
			if reference.MetadataTrusted && reference.DurationSeconds > 0 {
				if contract.MinReferenceVideoSeconds > 0 && reference.DurationSeconds < float64(contract.MinReferenceVideoSeconds) {
					return RejectMediaReferenceLimit
				}
				if contract.MaxReferenceVideoSeconds > 0 && reference.DurationSeconds > float64(contract.MaxReferenceVideoSeconds) {
					return RejectMediaReferenceLimit
				}
				totalVideoSeconds += reference.DurationSeconds
			}
		case MediaKindAudio:
			if !contract.ReferenceAudio {
				return RejectMediaReferenceUnsupported
			}
			if !mediaValueAllowedWhenDeclared(role, contract.AudioRoles) {
				return RejectMediaReferenceRole
			}
			if reference.MetadataTrusted && contract.MaxAudioBytes > 0 && reference.SizeBytes > contract.MaxAudioBytes {
				return RejectMediaReferenceLimit
			}
		}
	}
	if counts[MediaKindImage] < contract.MinImages ||
		(contract.MaxImages > 0 && counts[MediaKindImage] > contract.MaxImages) ||
		(contract.MaxVideos > 0 && counts[MediaKindVideo] > contract.MaxVideos) ||
		(contract.MaxAudios > 0 && counts[MediaKindAudio] > contract.MaxAudios) ||
		(contract.MaxTotalReferences > 0 && len(shape.References) > contract.MaxTotalReferences) ||
		(contract.MaxTotalReferenceVideoSeconds > 0 && totalVideoSeconds > float64(contract.MaxTotalReferenceVideoSeconds)) {
		return RejectMediaReferenceLimit
	}
	if shape.DurationKnown {
		duration := int(shape.DurationSeconds)
		switch normalizedMediaValue(contract.Duration.Mode) {
		case "fixed":
			if duration != contract.Duration.Fixed {
				return RejectMediaDurationMismatch
			}
		case "range":
			if duration < contract.Duration.Min || duration > contract.Duration.Max {
				return RejectMediaDurationMismatch
			}
			step := contract.Duration.Step
			if step <= 0 {
				step = 1
			}
			if (duration-contract.Duration.Min)%step != 0 {
				return RejectMediaDurationMismatch
			}
		}
	}
	if shape.OutputCountKnown && contract.OutputCount.Known {
		count := int(shape.OutputCount)
		if count < contract.OutputCount.Min || count > contract.OutputCount.Max {
			return RejectMediaOutputCountMismatch
		}
	}
	if !mediaValueAllowedWhenDeclared(shape.Resolution, contract.Resolutions) {
		return RejectMediaResolutionMismatch
	}
	if !mediaValueAllowedWhenDeclared(shape.AspectRatio, contract.AspectRatios) {
		return RejectMediaAspectRatioMismatch
	}
	if !mediaValueAllowedWhenDeclared(shape.Quality, contract.Qualities) {
		return RejectMediaQualityMismatch
	}
	return ""
}

func mediaValueAllowedWhenDeclared(value string, allowed []string) bool {
	value = normalizedMediaValue(value)
	if value == "" || len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if normalizedMediaValue(candidate) == value {
			return true
		}
	}
	return false
}

func normalizedMediaValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
