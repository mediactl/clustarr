/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package actions

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This file is Task G3-4's Settings-page writes: one merge-patch action per
// settings kind the page renders an edit form for (amendment §A3.4's
// "Settings" row; the G3-4 note under ruling R2). Every function here follows
// [SetMonitored]'s own shape -- a typed patch body, one JSON merge patch under
// [FieldManager], the apiserver's error wrapped so errors.Is/As still works --
// and the field set each one sends is deliberately the smallest the task
// allows: "fields a user plausibly changes (enabled flags, priorities,
// intervals, selected profile refs), not immutable or identity fields". A
// wider edit surface is left to a later task; CEL on an immutable field (e.g.
// QualityProfileSpec's builtIn-is-immutable rule) rejects a bad patch at the
// apiserver, which routes.go's finishAction already renders visibly through
// views.ActionError -- there is no need to pre-validate it here.
//
// Every settings kind gets its own entry in [Grants] (patch, alongside the
// group's existing read grants in config/rbac/ui_role.yaml) and its own
// envtest-free unit coverage in actions_test.go; cmd/clustarr/ui_rbac_test.go
// holds config/rbac/ui_role.yaml's write verbs to exactly what [Grants]
// declares, so a settings action with no matching grant fails that test
// rather than only failing silently in a real cluster.

// settingsGrants is [Grants]'s own contribution from this file: one patch
// grant per settings kind, appended to actions.go's create/monitor grants.
func settingsGrants() []Grant {
	return []Grant{
		{Group: catalogv1alpha1.GroupVersion.Group, Resource: "rootfolders", Verb: "patch"},
		{Group: catalogv1alpha1.GroupVersion.Group, Resource: "qualityprofiles", Verb: "patch"},
		{Group: catalogv1alpha1.GroupVersion.Group, Resource: "metadataproviders", Verb: "patch"},
		{Group: indexv1alpha1.GroupVersion.Group, Resource: "indexers", Verb: "patch"},
		{Group: downloadv1alpha1.GroupVersion.Group, Resource: "downloadclients", Verb: "patch"},
		{Group: subtitlev1alpha1.GroupVersion.Group, Resource: "subtitleproviders", Verb: "patch"},
		{Group: subtitlev1alpha1.GroupVersion.Group, Resource: "subtitleprofiles", Verb: "patch"},
		{Group: transcodev1alpha1.GroupVersion.Group, Resource: "transcodeprofiles", Verb: "patch"},
	}
}

// sendMergePatch marshals body -- a struct shaped {"spec":{...}} -- and sends
// it to obj as a JSON merge patch under [FieldManager], wrapping any
// apiserver error with what, obj's kind, namespace and name so
// errors.Is/As (apierrors.IsNotFound and friends) still work through the
// wrap. Every settings action in this file is this one call plus its own
// validation and its own typed patch body.
func sendMergePatch(ctx context.Context, p Patcher, obj client.Object, body any, what string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		// Unreachable for the fixed-shape structs below; kept so a future
		// field cannot turn a marshal failure into an empty patch.
		return fmt.Errorf("actions: encode %s patch: %w", what, err)
	}
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("actions: set %s on %s/%s: %w", what, obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// --- RootFolder: spec.scanSchedule -----------------------------------------

type rootFolderSchedulePatch struct {
	Spec struct {
		ScanSchedule string `json:"scanSchedule"`
	} `json:"spec"`
}

// SetRootFolderScanSchedule sets a RootFolder's spec.scanSchedule -- the cron
// expression importarr's RootFolder schedule reads to create periodic
// LibraryScans (empty means no periodic rescan). It is the one Settings-page
// field RootFolderSpec offers that is neither immutable (path and kind both
// carry a "self == oldSelf" CEL rule) nor a nested struct with no single
// obviously-safe leaf.
func SetRootFolderScanSchedule(
	ctx context.Context, p Patcher, namespace, name, schedule string,
) (*catalogv1alpha1.RootFolder, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetRootFolderScanSchedule")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body rootFolderSchedulePatch
	body.Spec.ScanSchedule = schedule

	obj := &catalogv1alpha1.RootFolder{}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "RootFolder scanSchedule"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: root folder scan schedule set",
		"namespace", namespace, "rootFolder", name, "scanSchedule", schedule)
	return obj, nil
}

// --- QualityProfile (cluster-scoped): spec.upgradeAllowed -------------------

type qualityProfileUpgradePatch struct {
	Spec struct {
		UpgradeAllowed bool `json:"upgradeAllowed"`
	} `json:"spec"`
}

// SetQualityProfileUpgradeAllowed sets a QualityProfile's
// spec.upgradeAllowed: whether an already-imported item may be replaced by a
// better release. QualityProfile is cluster-scoped, so there is no
// namespace. A built-in profile's own CEL rule ("built-in profiles are
// immutable; copy the profile instead") rejects this at the apiserver; the
// caller (ui/routes.go's finishAction) renders that rejection visibly rather
// than this function pre-checking spec.builtIn itself, which would just be a
// second, driftable copy of the same rule.
func SetQualityProfileUpgradeAllowed(
	ctx context.Context, p Patcher, name string, upgradeAllowed bool,
) (*catalogv1alpha1.QualityProfile, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetQualityProfileUpgradeAllowed")
	defer span.End()

	if name == "" {
		err := fmt.Errorf("%w: need a name (got %q)", ErrInvalid, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body qualityProfileUpgradePatch
	body.Spec.UpgradeAllowed = upgradeAllowed

	obj := &catalogv1alpha1.QualityProfile{}
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "QualityProfile upgradeAllowed"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: quality profile upgradeAllowed set",
		"qualityProfile", name, "upgradeAllowed", upgradeAllowed)
	return obj, nil
}

// --- Indexer: spec.enabled, spec.priority -----------------------------------

type enabledPriorityPatch struct {
	Spec struct {
		Enabled  bool  `json:"enabled"`
		Priority int32 `json:"priority"`
	} `json:"spec"`
}

// SetIndexerSettings sets an Indexer's spec.enabled and spec.priority -- the
// two fields §A3.4's Settings row calls out ("enabled flags, priorities")
// that Indexer has directly on its spec, alongside DownloadClient,
// MetadataProvider and SubtitleProvider below. Both fields are sent
// together, as one submission of one edit form, so a merge patch here never
// depends on a previous one's success.
func SetIndexerSettings(
	ctx context.Context, p Patcher, namespace, name string, enabled bool, priority int32,
) (*indexv1alpha1.Indexer, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetIndexerSettings")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body enabledPriorityPatch
	body.Spec.Enabled = enabled
	body.Spec.Priority = priority

	obj := &indexv1alpha1.Indexer{}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "Indexer enabled/priority"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: indexer settings set",
		"namespace", namespace, "indexer", name, "enabled", enabled, "priority", priority)
	return obj, nil
}

// --- DownloadClient: spec.enabled, spec.priority ----------------------------

// SetDownloadClientSettings is [SetIndexerSettings]'s DownloadClient
// counterpart.
func SetDownloadClientSettings(
	ctx context.Context, p Patcher, namespace, name string, enabled bool, priority int32,
) (*downloadv1alpha1.DownloadClient, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetDownloadClientSettings")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body enabledPriorityPatch
	body.Spec.Enabled = enabled
	body.Spec.Priority = priority

	obj := &downloadv1alpha1.DownloadClient{}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "DownloadClient enabled/priority"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: download client settings set",
		"namespace", namespace, "downloadClient", name, "enabled", enabled, "priority", priority)
	return obj, nil
}

// --- MetadataProvider: spec.enabled, spec.priority --------------------------

// SetMetadataProviderSettings is [SetIndexerSettings]'s MetadataProvider
// counterpart.
func SetMetadataProviderSettings(
	ctx context.Context, p Patcher, namespace, name string, enabled bool, priority int32,
) (*catalogv1alpha1.MetadataProvider, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetMetadataProviderSettings")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body enabledPriorityPatch
	body.Spec.Enabled = enabled
	body.Spec.Priority = priority

	obj := &catalogv1alpha1.MetadataProvider{}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "MetadataProvider enabled/priority"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: metadata provider settings set",
		"namespace", namespace, "metadataProvider", name, "enabled", enabled, "priority", priority)
	return obj, nil
}

// --- SubtitleProvider: spec.enabled, spec.priority --------------------------

// SetSubtitleProviderSettings is [SetIndexerSettings]'s SubtitleProvider
// counterpart.
func SetSubtitleProviderSettings(
	ctx context.Context, p Patcher, namespace, name string, enabled bool, priority int32,
) (*subtitlev1alpha1.SubtitleProvider, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetSubtitleProviderSettings")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body enabledPriorityPatch
	body.Spec.Enabled = enabled
	body.Spec.Priority = priority

	obj := &subtitlev1alpha1.SubtitleProvider{}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "SubtitleProvider enabled/priority"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: subtitle provider settings set",
		"namespace", namespace, "subtitleProvider", name, "enabled", enabled, "priority", priority)
	return obj, nil
}

// --- SubtitleProfile (cluster-scoped): spec.default -------------------------

type defaultPatch struct {
	Spec struct {
		Default bool `json:"default"`
	} `json:"spec"`
}

// SetSubtitleProfileDefault sets a SubtitleProfile's spec.default.
// SubtitleProfile is cluster-scoped, so there is no namespace. Only one
// profile may be the default; the controller, not this action, decides which
// one wins when two are marked true (SubtitleProfileConditionInvalid) -- the
// same division of labour as SetQualityProfileUpgradeAllowed's CEL rejection.
func SetSubtitleProfileDefault(
	ctx context.Context, p Patcher, name string, isDefault bool,
) (*subtitlev1alpha1.SubtitleProfile, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetSubtitleProfileDefault")
	defer span.End()

	if name == "" {
		err := fmt.Errorf("%w: need a name (got %q)", ErrInvalid, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body defaultPatch
	body.Spec.Default = isDefault

	obj := &subtitlev1alpha1.SubtitleProfile{}
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "SubtitleProfile default"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: subtitle profile default set",
		"subtitleProfile", name, "default", isDefault)
	return obj, nil
}

// --- TranscodeProfile (cluster-scoped): spec.priority -----------------------

type priorityPatch struct {
	Spec struct {
		Priority int32 `json:"priority"`
	} `json:"spec"`
}

// SetTranscodeProfilePriority sets a TranscodeProfile's spec.priority: jobs
// created from a higher-priority profile run first. TranscodeProfile is
// cluster-scoped, so there is no namespace.
func SetTranscodeProfilePriority(
	ctx context.Context, p Patcher, name string, priority int32,
) (*transcodev1alpha1.TranscodeProfile, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetTranscodeProfilePriority")
	defer span.End()

	if name == "" {
		err := fmt.Errorf("%w: need a name (got %q)", ErrInvalid, name)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body priorityPatch
	body.Spec.Priority = priority

	obj := &transcodev1alpha1.TranscodeProfile{}
	obj.SetName(name)
	if err := sendMergePatch(ctx, p, obj, body, "TranscodeProfile priority"); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: transcode profile priority set",
		"transcodeProfile", name, "priority", priority)
	return obj, nil
}

// --- Actions methods ---------------------------------------------------------

// SetRootFolderScanSchedule is [SetRootFolderScanSchedule] over the Actions'
// writer.
func (a *Actions) SetRootFolderScanSchedule(
	ctx context.Context, namespace, name, schedule string,
) (*catalogv1alpha1.RootFolder, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetRootFolderScanSchedule(ctx, a.w, namespace, name, schedule)
}

// SetQualityProfileUpgradeAllowed is [SetQualityProfileUpgradeAllowed] over
// the Actions' writer.
func (a *Actions) SetQualityProfileUpgradeAllowed(
	ctx context.Context, name string, upgradeAllowed bool,
) (*catalogv1alpha1.QualityProfile, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetQualityProfileUpgradeAllowed(ctx, a.w, name, upgradeAllowed)
}

// SetIndexerSettings is [SetIndexerSettings] over the Actions' writer.
func (a *Actions) SetIndexerSettings(
	ctx context.Context, namespace, name string, enabled bool, priority int32,
) (*indexv1alpha1.Indexer, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetIndexerSettings(ctx, a.w, namespace, name, enabled, priority)
}

// SetDownloadClientSettings is [SetDownloadClientSettings] over the Actions'
// writer.
func (a *Actions) SetDownloadClientSettings(
	ctx context.Context, namespace, name string, enabled bool, priority int32,
) (*downloadv1alpha1.DownloadClient, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetDownloadClientSettings(ctx, a.w, namespace, name, enabled, priority)
}

// SetMetadataProviderSettings is [SetMetadataProviderSettings] over the
// Actions' writer.
func (a *Actions) SetMetadataProviderSettings(
	ctx context.Context, namespace, name string, enabled bool, priority int32,
) (*catalogv1alpha1.MetadataProvider, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetMetadataProviderSettings(ctx, a.w, namespace, name, enabled, priority)
}

// SetSubtitleProviderSettings is [SetSubtitleProviderSettings] over the
// Actions' writer.
func (a *Actions) SetSubtitleProviderSettings(
	ctx context.Context, namespace, name string, enabled bool, priority int32,
) (*subtitlev1alpha1.SubtitleProvider, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetSubtitleProviderSettings(ctx, a.w, namespace, name, enabled, priority)
}

// SetSubtitleProfileDefault is [SetSubtitleProfileDefault] over the Actions'
// writer.
func (a *Actions) SetSubtitleProfileDefault(
	ctx context.Context, name string, isDefault bool,
) (*subtitlev1alpha1.SubtitleProfile, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetSubtitleProfileDefault(ctx, a.w, name, isDefault)
}

// SetTranscodeProfilePriority is [SetTranscodeProfilePriority] over the
// Actions' writer.
func (a *Actions) SetTranscodeProfilePriority(
	ctx context.Context, name string, priority int32,
) (*transcodev1alpha1.TranscodeProfile, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetTranscodeProfilePriority(ctx, a.w, name, priority)
}
