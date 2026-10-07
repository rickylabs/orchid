package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Owner selection is native routing data, not a new Harness catalog entry.
// The trusted grant binds Eric's standing delegation to one issue and whole brief.
type ownerNativeOverride struct {
	Authorizer string           `json:"authorizer"`
	Rationale  string           `json:"rationale"`
	Route      ownerNativeRoute `json:"route"`
}

type ownerNativeRoute struct {
	Harness  string `json:"harness"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

const ownerNativeSource = "orchid-owner-override"

// Syntax bounds only. No list of models or matrix families is consulted.
var ownerNativeModel = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._:/~-]{0,320}$`)

func (grant *MatrixGrant) UnmarshalJSON(raw []byte) error {
	// Explicit null must not silently turn an owner selection into an ordinary
	// matrix launch. This also keeps grant-input and runtime config decoding equal.
	type plainGrant MatrixGrant
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil || fields == nil {
		return matrixReason("override-invalid")
	}
	if native, present := fields["ownerNativeOverride"]; present && string(native) == "null" {
		return matrixReason("override-invalid")
	}
	var decoded plainGrant
	if strictJSON(raw, &decoded) != nil {
		return matrixReason("override-invalid")
	}
	*grant = MatrixGrant(decoded)
	return nil
}

func validOwnerNativeOverride(grant *ownerNativeOverride) bool {
	if grant == nil || grant.Authorizer != "eric" || !cleanText(grant.Rationale) || len(grant.Rationale) > 1024 {
		return false
	}
	route := grant.Route
	if !containsString(matrixTransports, route.Harness) || !openCodeProviderID.MatchString(route.Provider) ||
		!ownerNativeModel.MatchString(route.Model) || strings.Contains(route.Model, "..") ||
		!validNativeEffort(route.Effort) {
		return false
	}
	if route.Harness == "opencode" {
		native, err := resolveOpenCodeRoute(Overrides{Model: route.Model, Router: route.Provider, Effort: route.Effort})
		return err == nil && native.qualifiedModel() == route.Model
	}
	return true
}

func resolveLaunchRoute(ctx context.Context, cfg MatrixConfig, req matrixRequest,
	resolve func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error)) (matrixRoute, error) {
	if req.NativeOverride == nil {
		return resolve(ctx, cfg, req)
	}
	grant := req.NativeOverride
	if req.Override != nil || !validOwnerNativeOverride(grant) {
		return matrixRoute{}, matrixReason("override-invalid")
	}
	if !budgetTierPattern.MatchString(req.Tier) || !profileStem.MatchString(strings.ReplaceAll(req.Role, "_", "-")) {
		return matrixRoute{}, matrixReason("routing-invalid")
	}
	native := grant.Route
	if req.Pin != nil && ((req.Pin.Model != "" && req.Pin.Model != native.Model) ||
		(req.Pin.Effort != "" && req.Pin.Effort != native.Effort)) {
		return matrixRoute{}, matrixReason("override-invalid")
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return matrixRoute{}, matrixReason("override-invalid")
	}
	return matrixRoute{Provider: native.Provider, Model: native.Model, Effort: native.Effort,
		RequestedEffort: native.Effort, Transport: native.Harness, Tier: req.Tier, Role: req.Role,
		Digest: shaText(encoded)}, nil
}

func receiptForOwnerNative(route matrixRoute, grant *ownerNativeOverride) matrixReceipt {
	// Reuse the fixed unknown-observation vocabulary, without attributing selection
	// to a matrix revision or inventing a logical model/family for a native ID.
	receipt := receiptFor(MatrixConfig{}, route)
	receipt.Resolution.SourceRepository = ownerNativeSource
	receipt.OwnerNativeOverride = grant
	return receipt
}
