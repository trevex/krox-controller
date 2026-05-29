//go:build tools

/*
Copyright 2026 krox-controller authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package tools tracks Go module dependencies that are not yet referenced
// from non-test source files but are required for upcoming tasks. The
// `tools` build tag keeps these blank imports out of normal builds while
// preventing `go mod tidy` from pruning the modules from go.mod.
//
// Once the corresponding production code lands (KRO RGD parsing/runtime,
// Flux source artifact handling, e2e suite wiring), the blank imports
// here can be removed.
package tools

import (
	// KRO: graph/runtime + v1alpha1 ResourceGraphDefinition types used by
	// internal/render and the reconciler.
	_ "github.com/kubernetes-sigs/kro/api/v1alpha1"

	// Flux source-controller: GitRepository / OCIRepository v1 types used
	// by the artifact fetch path.
	_ "github.com/fluxcd/source-controller/api/v1"

	// e2e-framework: kind-based end-to-end harness used by test/e2e.
	_ "sigs.k8s.io/e2e-framework/pkg/env"
)
