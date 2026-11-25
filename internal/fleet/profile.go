package fleet

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/theoneLee/vllm-router-lab/internal/kvcache"
	"github.com/theoneLee/vllm-router-lab/protocol"
)

type profile struct {
	Version         string
	SourceDiscovery bool `json:"source_discovery"`
	FixedRoot       bool `json:"fixed_cbor_root"`
}

func selectProfile(version string) (profile, error) {
	var profiles []profile
	if err := json.Unmarshal(protocol.Versions, &profiles); err != nil {
		return profile{}, err
	}
	for _, p := range profiles {
		if p.Version == strings.TrimPrefix(version, "v") {
			return p, nil
		}
	}
	return profile{}, fmt.Errorf("unknown version %q; see protocol/versions.json", version)
}

func (p profile) hashRoot(seed string) (kvcache.Digest, error) {
	if seed != "" {
		if _, err := strconv.ParseUint(seed, 10, 32); err != nil {
			return kvcache.Digest{}, fmt.Errorf("invalid numeric hash seed %q", seed)
		}
		return kvcache.HashCBOR(seed), nil
	}
	if p.FixedRoot {
		return kvcache.DefaultRoot, nil
	}
	var r kvcache.Digest
	_, err := rand.Read(r[:])
	return r, err
}
