package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed protocol/versions.json
var versionManifest []byte

type profile struct {
	Version         string
	SourceDiscovery bool `json:"source_discovery"`
	FixedRoot       bool `json:"fixed_cbor_root"`
}

func selectProfile(version string) (profile, error) {
	var profiles []profile
	if err := json.Unmarshal(versionManifest, &profiles); err != nil {
		return profile{}, err
	}
	for _, p := range profiles {
		if p.Version == strings.TrimPrefix(version, "v") {
			return p, nil
		}
	}
	return profile{}, fmt.Errorf("unknown version %q; see protocol/versions.json", version)
}

func (p profile) hashRoot(seed string) (digest, error) {
	if seed != "" {
		if _, err := strconv.ParseUint(seed, 10, 32); err != nil {
			return digest{}, fmt.Errorf("invalid numeric hash seed %q", seed)
		}
		return hashCBOR(seed), nil
	}
	if p.FixedRoot {
		return root, nil
	}
	var r digest
	_, err := rand.Read(r[:])
	return r, err
}
