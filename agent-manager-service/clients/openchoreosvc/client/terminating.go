// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package client

import (
	"errors"
	"fmt"

	"github.com/wso2/agent-manager/agent-manager-service/clients/openchoreosvc/gen"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

// isTerminating reports whether a resource has been asked to go away but has not yet
// left the API.
//
// Deleting an OpenChoreo resource only sets metadata.deletionTimestamp: the object stays
// in the Kubernetes API, and therefore in every LIST response, until its cleanup finalizer
// (openchoreo.dev/project-cleanup and friends) has torn down the underlying builds,
// releases and workloads. That can take minutes. Listing such an object is wrong twice
// over: the console keeps rendering a resource the user just deleted, and the "is this
// still in use" guards (CountProjectComponents, ListComponentsByKind) refuse a delete on
// the strength of a dependent that is itself already on its way out.
//
// Every list in this package that returns user-visible resources or feeds one of those
// guards filters on this, so a delete reads as done the moment OpenChoreo accepts it.
func isTerminating(meta gen.ObjectMeta) bool {
	return meta.DeletionTimestamp != nil
}

// terminatingConflict refines a create conflict. OpenChoreo answers a create with 409
// both when the name is genuinely taken and when the previous holder of the name has
// been deleted but is still finalizing — and the latter is invisible to the user,
// because every list filters it out (see isTerminating). When createErr is a conflict
// and lookup finds the existing object terminating, the conflict is replaced with
// ErrResourceBeingDeleted so the caller can say "try again shortly" instead of
// "already exists". Any other error, or a failed lookup, returns createErr unchanged.
func terminatingConflict(createErr error, kind, name string, lookup func() (*gen.ObjectMeta, error)) error {
	if !errors.Is(createErr, utils.ErrConflict) {
		return createErr
	}
	meta, err := lookup()
	if err != nil || meta == nil || !isTerminating(*meta) {
		return createErr
	}
	return fmt.Errorf("%w: %s %q is still being cleaned up, try again shortly", utils.ErrResourceBeingDeleted, kind, name)
}
