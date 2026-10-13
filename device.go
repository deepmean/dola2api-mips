package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// DeviceIdentity is the set of stable identifiers the web client sends with
// every write.
//
// These are the one measured difference between a request that works and one
// that is refused. Captured from the browser while it answered normally:
//
//	device_id            localStorage samantha_web_web_id.web_id
//	web_id, tea_uuid     localStorage __tea_cache_tokens_495671.web_id / user_unique_id
//	fp                   localStorage s_v_web_id — note this differs from the
//	                     s_v_web_id cookie, and the working request sent the
//	                     localStorage one
//	tz_name              the browser's timezone
//
// The port sent none of them, plus a channel parameter the client does not send,
// plus a language the client leaves empty. Those are the differences worth
// closing before looking anywhere else.
type DeviceIdentity struct {
	DeviceID string `json:"device_id"`
	WebID    string `json:"web_id"`
	TeaUUID  string `json:"tea_uuid"`
	// Fingerprint is the s_v_web_id the client keeps in localStorage.
	Fingerprint string `json:"fp"`
	TZName      string `json:"tz_name"`
}

// newDeviceIdentity generates a plausible identity.
//
// The ids are 19-digit numbers, which is what the client's SDK produces. They
// only have to be stable and well-formed: the upstream treats them as an
// opaque device handle, and a value invented once and kept is what a real
// device looks like.
func newDeviceIdentity(tz string) (DeviceIdentity, error) {
	deviceID, err := randomDigits(19)
	if err != nil {
		return DeviceIdentity{}, err
	}
	webID, err := randomDigits(19)
	if err != nil {
		return DeviceIdentity{}, err
	}
	tea, err := randomDigits(19)
	if err != nil {
		return DeviceIdentity{}, err
	}
	fp, err := randomFingerprint()
	if err != nil {
		return DeviceIdentity{}, err
	}
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	return DeviceIdentity{DeviceID: deviceID, WebID: webID, TeaUUID: tea, Fingerprint: fp, TZName: tz}, nil
}

// randomDigits returns n random decimal digits, without a leading zero.
func randomDigits(n int) (string, error) {
	out := make([]byte, n)
	first, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return "", fmt.Errorf("device: randomness unavailable: %w", err)
	}
	out[0] = byte('1' + first.Int64())
	for i := 1; i < n; i++ {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("device: randomness unavailable: %w", err)
		}
		out[i] = byte('0' + d.Int64())
	}
	return string(out), nil
}

// randomFingerprint builds a value shaped like the client's: the literal
// "verify_", then four base62 groups of decreasing length separated by
// underscores. The upstream does not check the contents, only that a device has
// one and keeps it.
func randomFingerprint() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	groups := []int{8, 8, 4, 4, 4, 16}
	parts := make([]string, 0, len(groups))
	for _, n := range groups {
		buf := make([]byte, n)
		for i := range buf {
			idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", fmt.Errorf("device: randomness unavailable: %w", err)
			}
			buf[i] = alphabet[idx.Int64()]
		}
		parts = append(parts, string(buf))
	}
	out := "verify"
	for _, p := range parts {
		out += "_" + p
	}
	return out, nil
}

// LoadDeviceIdentity reads the identity, creating and saving one if the file is
// absent.
//
// It is persisted for the same reason the client persists it: a device that
// presents a new identifier on every request is not a device, and the upstream's
// risk scoring treats it accordingly.
func LoadDeviceIdentity(path, tz string) (DeviceIdentity, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var identity DeviceIdentity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return DeviceIdentity{}, fmt.Errorf("device: %s is not readable: %w", path, err)
		}
		// Fill in anything an older file lacks rather than failing.
		changed := false
		if identity.DeviceID == "" || identity.WebID == "" || identity.TeaUUID == "" || identity.Fingerprint == "" {
			fresh, err := newDeviceIdentity(tz)
			if err != nil {
				return DeviceIdentity{}, err
			}
			if identity.DeviceID == "" {
				identity.DeviceID = fresh.DeviceID
			}
			if identity.WebID == "" {
				identity.WebID = fresh.WebID
			}
			if identity.TeaUUID == "" {
				identity.TeaUUID = fresh.TeaUUID
			}
			if identity.Fingerprint == "" {
				identity.Fingerprint = fresh.Fingerprint
			}
			changed = true
		}
		if identity.TZName == "" {
			identity.TZName = tz
			changed = true
		}
		if changed {
			if err := saveDeviceIdentity(path, identity); err != nil {
				return DeviceIdentity{}, err
			}
		}
		return identity, nil
	case os.IsNotExist(err):
		identity, err := newDeviceIdentity(tz)
		if err != nil {
			return DeviceIdentity{}, err
		}
		if err := saveDeviceIdentity(path, identity); err != nil {
			return DeviceIdentity{}, err
		}
		return identity, nil
	default:
		return DeviceIdentity{}, fmt.Errorf("device: read %s: %w", path, err)
	}
}

// saveDeviceIdentity writes the identity atomically, mode 0600.
func saveDeviceIdentity(path string, identity DeviceIdentity) error {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("device: encode: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".device-*.json")
	if err != nil {
		return fmt.Errorf("device: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("device: chmod: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("device: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("device: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("device: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("device: rename: %w", err)
	}
	return nil
}

// localTimezone reports the machine's timezone name, falling back to the value
// the client sends.
func localTimezone() string {
	if name := time.Now().Location().String(); name != "" && name != "Local" {
		return name
	}
	return "Asia/Shanghai"
}
