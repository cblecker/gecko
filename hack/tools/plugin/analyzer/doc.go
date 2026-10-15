/*
Package analyzer implements golangci-lint analysis passes that enforce the
platform API conventions documented in platform-api/API-CONVENTIONS.md.

The analyzers in this package are composed into the "geckoconventions"
golangci-lint plugin (hack/tools/plugin/plugin.go). They run alongside
kube-api-linter and cover rules from API-CONVENTIONS.md that are not
already enforced by kube-api-linter or the general linters enabled in
platform-api/.golangci.yml.

# Checks implemented

## requirednoomitempty

Convention: "+required MUST NOT have omitempty."

Fields marked +required must be present in every serialised object. The
omitempty JSON tag option suppresses zero-valued fields from serialisation,
which would silently drop a required field if it holds its zero value.
This check reports any struct field that carries both +required in its doc
comment and omitempty in its JSON tag.

## noimmutablemarker

Convention: "Do not use +immutable — it has no defined semantics in
Kubebuilder."

The +immutable marker is not processed by controller-gen or any other tool
in the chain. Immutability must be expressed with a CEL XValidation rule:

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="field is immutable"

This check reports any occurrence of the bare +immutable marker on a field
or type.

## preferstandardmarkers

Convention: "Prefer standard markers over kubebuilder-specific equivalents."

Several kubebuilder-specific marker variants have standard equivalents that
work with a wider range of generators and are toolchain-agnostic. This check
reports the kubebuilder form and suggests the standard replacement:

	+kubebuilder:default=<v>          → +default=<v>
	+kubebuilder:validation:Optional  → +optional
	+kubebuilder:validation:Required  → +required
	+kubebuilder:validation:ListType  → +listType

## noisprefixbool

Convention: "Booleans: Never IsFoo; use Foo."

bool fields whose Go name starts with "Is" followed by an upper-case letter
(e.g. IsEnabled) violate the naming convention. The "Is" prefix adds no
semantic value and diverges from established Kubernetes field naming. This
check reports such fields and suggests the unprefixed name.

Note: the check is limited to plain bool and *bool fields. String-alias
boolean types (recommended by the conventions for cases where a third state
is plausible) are not reported.

## timestampnaming

Convention: "Timestamps: somethingTime (metav1.Time). Not At, Stamp, or
Timestamp."

metav1.Time and *metav1.Time fields whose name ends with "At", "Stamp", or
"Timestamp" violate the naming convention. This check reports such fields
and suggests the "Time"-suffixed replacement name.

## optionalhasomitempty

Convention: "+optional MUST have omitempty."

Fields marked +optional must carry omitempty in their JSON tag so that
unset fields are omitted from serialised objects. Without omitempty, a
zero-valued optional field is serialised as its zero value (e.g. "" or 0),
which clients cannot distinguish from an explicitly set value.

Only exported fields with a JSON struct tag are checked. Plain metav1.Time
fields (without a pointer wrapper) are skipped: encoding/json does not call
IsZero() on plain structs, so omitempty has no effect on them and the
idiomatic fix is to use *metav1.Time instead — which is already flagged by
kube-api-linter's optionalfields linter. This check does not duplicate the
pointer/nilable-type enforcement; it only verifies the omitempty tag.

## scalarbounds

Convention: "Scalar bounds MUST always be declared as markers regardless of
where other validation lives."

Fields of the following types must carry the corresponding bound markers:

  - string / *string      → +kubebuilder:validation:MaxLength=<N>
  - int32 / int64 (and their pointer forms)
    → at least one of +kubebuilder:validation:Minimum=<N> or
      +kubebuilder:validation:Maximum=<N>
  - []T (slice)           → +kubebuilder:validation:MaxItems=<N>
  - map[K]V               → +kubebuilder:validation:MaxProperties=<N>

The check only verifies that the marker is present; it does not enforce
specific values. Only exported fields with a JSON struct tag are inspected.
Fields excluded from serialisation (json:"-") and non-string/int/slice/map
types (bool, struct, metav1.Time, …) are skipped.

# Checks not yet implemented

The following rules from API-CONVENTIONS.md are not (yet) enforced by this
plugin. Reasons are given where the check is non-trivial.

## Field optionality — +optional without nilable/pointer type

Convention: "+optional MUST have omitempty and use a pointer or nilable type."

kube-api-linter's optionalfields linter partially covers this (configured
as Warn in .golangci.yml). A companion check here would be redundant while
the kube-api-linter version is enabled, though it could be promoted to an
error-level finding once the existing baseline violations are resolved.

## Every field must have a doc comment starting with the JSON field name

Convention: "every field MUST have a comment starting with the JSON field
name."

kube-api-linter's commentstart linter covers this exactly, but it is
currently disabled in .golangci.yml with the note that fixing it requires a
large separate effort. Once that work is done, re-enabling commentstart is
preferred over a duplicate check here.

## Public field doc-strings must not contain internal information

Convention: "Comments on +orlop:public fields MUST NOT contain internal
implementation details, management-cluster names, RH-internal abbreviations,
or operator-tuning information."

This is a semantic check that requires a configurable allow/deny list of
patterns and knowledge of which fields carry +orlop:public. It is feasible
to implement but requires agreement on the prohibited pattern list before
it can be made non-noisy. Tracked as a follow-up.

## Scalar bounds — value enforcement

Convention: "Scalar bounds MUST always be declared as markers."

The scalarbounds analyzer (above) checks that bound markers are present but
does not enforce their values. Whether the chosen MaxLength, Minimum,
Maximum, MaxItems, or MaxProperties value is appropriate for the field
cannot be determined statically; it requires domain knowledge. Value
enforcement is left to code review.

## CEL error messages must describe the valid form, not just flag invalid input

Convention: "CEL error messages MUST describe what the value must look like,
not just that it is invalid."

Checking this would require parsing the message= argument of every
+kubebuilder:validation:XValidation marker and applying heuristics to
classify it as prescriptive vs. diagnostic. The heuristics are inherently
approximate and would produce false positives. Not implemented.

## Boolean fields: prefer string alias when a third state is plausible

Convention: "Booleans: prefer a string alias when a third state is
plausible."

Detecting "plausible third state" requires understanding domain semantics,
which cannot be determined from the type alone. Not automatable.

## No abbreviations except id and url

Convention: "No abbreviations except well-established ones (id, url)."

A comprehensive abbreviation check requires a curated exception list and
produces many false positives on legitimate short names (e.g. GCP, DNS,
URL). Not implemented.

## Resource structure invariants

Convention: "+kubebuilder:subresource:status MUST be present whenever Status
exists; both the resource type and its list type MUST be registered in
init()."

kube-api-linter's statussubresource linter covers the subresource:status
requirement. The init() registration requirement is not checked by any
current linter and would require cross-file analysis to verify that every
root type registered in its package's init() has a corresponding List type
also registered.

## Floats must not appear in Spec

Convention: "Floats MUST NOT appear in Spec."

kube-api-linter's nofloats linter covers this exactly. Already enabled.

## time.Duration type forbidden

Convention: "Durations: fooSeconds (int32/int64). Never time.Duration."

kube-api-linter's nodurations linter covers this exactly. Already enabled.

## int type forbidden; unsigned integers forbidden

Convention: "Integers: int32 or int64. Never int or unsigned types."

kube-api-linter's integers linter covers this exactly. Already enabled.

## All slice fields must declare +listType

Convention: "All slice fields MUST declare +listType."

kube-api-linter's ssatags linter covers list type markers. Already enabled.

## Conditions field must use metav1.Condition with specific markers

Convention: Conditions must use +listType=map, +listMapKey=type,
+patchStrategy=merge, +patchMergeKey=type.

kube-api-linter's conditions linter covers this. Already enabled.

## phase field pattern deprecated

Convention: "The phase field pattern is deprecated."

kube-api-linter's nophase linter covers this. Already enabled.
*/
package analyzer
