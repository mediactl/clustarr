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

package indexarr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The facade's API-key Secret: read, and created when it is absent. get and
// create only -- indexarr never updates or patches it, because a key an
// operator set must never be overwritten by one this process generated.
// app/indexer/controller/indexer already grants secrets get;create;patch for the
// Cardigann session Secret; this package asks for what it uses on its own,
// so the grant survives that one changing.
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create

// FacadeAPIKeyField is the data key a Secret [ensureFacadeAPIKeys] generates
// stores its key under. It is only the generated key's name: the facade
// accepts the value of EVERY non-blank entry in the Secret, so an operator
// rotates without downtime by adding a second entry, moving clients over,
// then deleting the first -- each step followed by a restart, because the
// keys are read once at startup.
const FacadeAPIKeyField = "apikey"

// facadeAPIKeyBytes is a generated key's entropy: 32 random bytes, sent as
// 64 hex characters, which is the shape every Torznab client's apikey field
// accepts.
const facadeAPIKeyBytes = 32

// ensureFacadeAPIKeys returns the API keys the Torznab facade accepts: the
// non-blank values of Secret namespace/name, which it creates with one random
// key first when the Secret does not exist.
//
// # Why generate rather than require
//
// The facade fails closed (facade.New refuses to build with no key), and the
// alternative to generating is requiring an operator to create the Secret
// before indexarr will start. That turns an optional feature into a hard
// prerequisite of the whole indexer service -- search, RSS and grabs
// included -- on every install path: config/, the chart and kind alike.
// Prowlarr, Sonarr and Radarr all generate their API key on first start for
// the same reason. The operator reads it back with
//
//	kubectl -n <ns> get secret <name> -o jsonpath='{.data.apikey}' | base64 -d
//
// # What it never does
//
// It never modifies an existing Secret. A Secret that exists with no
// non-blank entry is an error, not an invitation to add one: an operator
// who emptied it meant something, and the facade must not come back up with
// a key they never saw. It reads through reader -- the manager's API reader,
// live, since indexarr never caches Secrets (see Options.ManagerOptions) --
// and never logs a key.
func ensureFacadeAPIKeys(
	ctx context.Context, reader client.Reader, writer client.Writer, namespace, name string,
) ([]string, error) {
	log := logging.FromContext(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: name}

	var sec corev1.Secret
	err := reader.Get(ctx, key, &sec)
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		generated, gerr := newFacadeAPIKey()
		if gerr != nil {
			return nil, gerr
		}
		sec = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{"app.kubernetes.io/component": ServiceName},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{FacadeAPIKeyField: []byte(generated)},
		}
		cerr := writer.Create(ctx, &sec, client.FieldOwner(k8s.ManagerIndexarr.String()))
		switch {
		case cerr == nil:
			log.Info("indexarr: generated the Torznab facade's API key; read it from the Secret",
				"secret", key.String(), "field", FacadeAPIKeyField)
		case apierrors.IsAlreadyExists(cerr):
			// Someone created it between the Get and the Create -- an
			// operator, or a previous pod of this Recreate Deployment that
			// had not finished exiting. Theirs wins: read it back.
			if err := reader.Get(ctx, key, &sec); err != nil {
				return nil, fmt.Errorf("indexarr: re-read the facade API-key Secret %s: %w", key, err)
			}
		default:
			return nil, fmt.Errorf("indexarr: create the facade API-key Secret %s: %w", key, cerr)
		}
	default:
		return nil, fmt.Errorf("indexarr: read the facade API-key Secret %s: %w", key, err)
	}

	keys := facadeAPIKeys(&sec)
	if len(keys) == 0 {
		return nil, fmt.Errorf("indexarr: the facade API-key Secret %s has no non-blank entry, and the Torznab "+
			"facade will not serve without a key; add one, delete the Secret to have a key generated, or set "+
			"--facade-bind-address=%s to disable the facade", key, k8s.DisabledBindAddress)
	}
	return keys, nil
}

// facadeAPIKeys returns the trimmed, non-blank values of sec's data, in key
// order so a restart builds the same list.
func facadeAPIKeys(sec *corev1.Secret) []string {
	names := make([]string, 0, len(sec.Data))
	for n := range sec.Data {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if v := strings.TrimSpace(string(sec.Data[n])); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// newFacadeAPIKey returns facadeAPIKeyBytes of crypto/rand, hex-encoded.
func newFacadeAPIKey() (string, error) {
	b := make([]byte, facadeAPIKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("indexarr: generate the facade API key: %w", err)
	}
	return hex.EncodeToString(b), nil
}
