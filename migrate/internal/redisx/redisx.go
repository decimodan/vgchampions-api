package redisx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Payload holds JSON payloads for one tournament.
type Payload struct {
	ID         string
	DetailsRaw string // JSON object
	ListRaw    string // JSON array or empty
}

// CollectIDs SCANs Redis for *:tournament:*:details keys under prefix.
func CollectIDs(ctx context.Context, client *redis.Client, prefix string) ([]string, error) {
	suffixPattern := fmt.Sprintf("%s:tournament:*:details", prefix)
	var ids []string
	var cur uint64
	for {
		keys, next, err := client.Scan(ctx, cur, suffixPattern, 512).Result()
		if err != nil {
			return nil, err
		}
		pre := prefix + ":tournament:"
		for _, k := range keys {
			id := detailsKeyToID(k, pre)
			if id != "" {
				ids = append(ids, id)
			}
		}
		cur = next
		if cur == 0 {
			break
		}
	}
	return uniqueSorted(ids), nil
}

func detailsKeyToID(key, pre string) string {
	if !strings.HasPrefix(key, pre) {
		return ""
	}
	rest := strings.TrimPrefix(key, pre)
	if !strings.HasSuffix(rest, ":details") {
		return ""
	}
	return strings.TrimSuffix(rest, ":details")
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func DetailsKey(prefix, id string) string {
	return fmt.Sprintf("%s:tournament:%s:details", prefix, id)
}

func StandingsKey(prefix, id string) string {
	return fmt.Sprintf("%s:tournament:%s:standings", prefix, id)
}

// FetchPayload retrieves details (required) and standings (optional).
func FetchPayload(ctx context.Context, client *redis.Client, prefix, id string) (*Payload, error) {
	dk := DetailsKey(prefix, id)
	sk := StandingsKey(prefix, id)

	detail, err := client.Get(ctx, dk).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	var list string
	s, err := client.Get(ctx, sk).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	if err == nil {
		list = s
	}

	return &Payload{
		ID:         id,
		DetailsRaw: detail,
		ListRaw:    list,
	}, nil
}
