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

package rowcache

import (
	"os"

	clientfeatures "k8s.io/client-go/features"
)

// The manager talks to the apiserver in CBOR (owner, 2026-11-04: "Enable
// ClientsAllowCBOR and ClientsPreferCBOR"). talos's apiserver serves and
// accepts it (CBORServingAndStorage), and client-go 0.37 negotiates it
// behind its two gates: ClientsAllowCBOR lets a client decode CBOR answers
// and streams, ClientsPreferCBOR makes a client whose content type is left
// to the default ask for CBOR first and send its bodies in it. Neither
// reached controller-runtime 0.25's clients: it built the codec factories
// of its client and its cache without the CBOR serializer, so under
// PreferCBOR every list asked for CBOR and could not decode the answer
// ("serializer for application/cbor doesn't exist"; at 04:05Z and again at
// 04:24Z the manager pod started with the two variables never became
// ready). go.mod replaces controller-runtime with the clustarr fork
// (github.com/mediactl/controller-runtime, one commit on v0.25.1), whose two
// codec factories go through rest.CodecFactoryForGeneratedClient, client-go's
// own helper that adds the CBOR serializer when ClientsAllowCBOR is on.
//
// Both gates are turned on here, at init, through client-go's own gates: a
// value set this way outranks the environment, so each is set only when the
// environment does not name it (KUBE_FEATURE_ClientsAllowCBOR,
// KUBE_FEATURE_ClientsPreferCBOR), which keeps the environment's say and
// client-go's test helper working as they do. With a gate off, client-go
// negotiates as before: JSON, and protobuf where a built-in kind allows it.

func init() {
	s, ok := clientfeatures.FeatureGates().(interface {
		Set(clientfeatures.Feature, bool) error
	})
	if !ok {
		return
	}
	for _, f := range []clientfeatures.Feature{clientfeatures.ClientsAllowCBOR, clientfeatures.ClientsPreferCBOR} {
		if _, named := os.LookupEnv("KUBE_FEATURE_" + string(f)); named {
			continue
		}
		_ = s.Set(f, true)
	}
}
