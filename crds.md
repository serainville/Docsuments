# Kubernetes CRD API Version Deprecation and Removal

**Audience:** Platform engineers  
**Example API:** `widgets.example.io`  
**Version lifecycle:** `v1alpha1` â†’ `v1beta1`

## 1. Purpose

This document explains how Kubernetes handles versioning, deprecation, storage migration, conversion, and removal for APIs defined through a `CustomResourceDefinition` (CRD).

The example assumes that a third-party controller:

1. Initially provides a `v1alpha1` API.
2. Introduces `v1beta1` in a later release.
3. Marks `v1alpha1` as deprecated while it remains available.
4. Stops serving `v1alpha1`.
5. Removes `v1alpha1` from the CRD in a future release.

The central distinction is that an API version's **client-facing representation**, **served state**, **deprecation state**, and **storage role** are separate concerns.

---

## 2. Executive Summary

A CRD can define multiple API versions at the same time, but exactly one version must have `storage: true`.

- `served: true` exposes a version through the Kubernetes REST API.
- `storage: true` defines the schema Kubernetes uses for new and updated objects in persistent storage.
- `deprecated: true` adds a warning to requests made through that version; it does not disable the version.
- A conversion strategy translates objects between the version requested by a client and the version used for storage.
- Changing the storage version does not automatically rewrite existing objects.
- Any successful write, including a write to the `/status` subresource, stores the object using the current storage version.
- Reads do not migrate stored objects.
- Setting an old version to `served: false` stops clients from using that API endpoint but does not delete the custom resources.
- An old version should not be removed from `spec.versions` until stored objects have been migrated and the version is no longer listed in `status.storedVersions`.

---

## 3. CRD Version Controls

Each entry under `spec.versions` defines an API representation of the custom resource.

| Field | Purpose |
|---|---|
| `name` | API version name, such as `v1alpha1` or `v1beta1`. |
| `served` | Controls whether the API server exposes the version through REST and API discovery. |
| `storage` | Identifies the version used to persist new or updated objects. Exactly one version must be `true`. |
| `deprecated` | Causes API requests to the version to receive a deprecation warning. |
| `deprecationWarning` | Optionally replaces the default deprecation warning. |
| `schema` | Defines validation, defaulting, and field-pruning behavior for that version. |

The CRD also contains:

| Field | Purpose |
|---|---|
| `spec.conversion.strategy` | Selects `None` or `Webhook` conversion. |
| `status.storedVersions` | Tracks versions that have been used to persist objects and might still exist in storage. |

### 3.1 `served` and `storage` are independent

A version can be:

- Served and used for storage.
- Served but not used for storage.
- Not served but retained in the CRD because objects might still be stored in that version.

For example:

```yaml
spec:
  versions:
    - name: v1alpha1
      served: true
      storage: false
    - name: v1beta1
      served: true
      storage: true
```

Clients can submit and retrieve either version, but all successful creates and updates are persisted using the `v1beta1` storage representation.

---

## 4. API Representation Versus Storage Representation

The `apiVersion` in a client manifest identifies the API endpoint and schema through which the client communicates. It does not necessarily identify the encoding currently stored in the Kubernetes backing store.

Assume the following object is submitted:

```yaml
apiVersion: example.io/v1alpha1
kind: Widget
metadata:
  name: example
spec:
  target: service-a
```

If `v1beta1` is the storage version, the API server performs the following logical sequence:

```text
v1alpha1 request
       |
       v
Validate the requested representation
       |
       v
Convert v1alpha1 to v1beta1
       |
       v
Persist using the v1beta1 storage schema
```

When a client later requests the object through `v1alpha1`, Kubernetes converts the stored `v1beta1` representation back to `v1alpha1` before returning it:

```text
Stored v1beta1 object
       |
       v
Convert v1beta1 to requested v1alpha1
       |
       v
Return v1alpha1 response
```

Consequently, a normal `kubectl get` shows the version requested or selected by the API client, not reliable evidence of the object's underlying storage version.

---

## 5. Conversion Strategies

A CRD selects one conversion strategy for conversions among all versions defined by that CRD.

### 5.1 `None`

```yaml
spec:
  conversion:
    strategy: None
```

With `None`, Kubernetes changes the `apiVersion` of the representation but does not perform custom field transformations.

This strategy is appropriate only when the versions are structurally compatible and fields have the same names, types, and meanings.

It is not sufficient for changes such as:

- Renaming a field.
- Splitting one field into several fields.
- Combining several fields.
- Changing a field's type.
- Changing the semantic meaning of a value.
- Preserving data that exists in only one version.

### 5.2 `Webhook`

```yaml
spec:
  conversion:
    strategy: Webhook
    webhook:
      conversionReviewVersions:
        - v1
      clientConfig:
        service:
          namespace: widget-system
          name: widget-conversion-webhook
          path: /convert
          port: 443
        caBundle: <base64-encoded-ca-bundle>
```

A conversion webhook contains the vendor's conversion logic. The API server invokes it whenever an object must be translated between API representations.

Typical examples include:

- Converting a client request into the storage version.
- Converting a stored object into the version requested by a client.
- Returning list or watch results in the requested version.
- Rewriting objects during a storage-version migration.

### 5.3 Purpose of a conversion webhook

The webhook allows multiple API versions with different schemas to represent the same logical resource.

For example:

```yaml
# v1alpha1
spec:
  backend: service-a
```

```yaml
# v1beta1
spec:
  destination:
    serviceName: service-a
```

The webhook can map `spec.backend` to `spec.destination.serviceName` and perform the inverse conversion when required.

A conversion webhook is not:

- An admission webhook.
- A controller reconciliation webhook.
- A background storage-migration mechanism.
- A signal that every existing object has already been rewritten.

It converts representations when the API server requests conversion. It does not independently scan and update objects.

### 5.4 Operational dependency

While multiple schemas or old stored representations remain in use, the conversion webhook becomes part of the API-serving path.

If it is unavailable or no longer supports a required version, operations that require conversion can fail, including:

- Reads.
- Lists and watches.
- Creates and updates.
- Status updates.
- Controller reconciliation.
- Storage migration.

The webhook must remain available and support the old version until the storage migration and client migration are complete.

---

## 6. Version Lifecycle

The following lifecycle separates compatibility, deprecation, storage migration, and removal into explicit stages.

## 6.1 Stage 1: `v1alpha1` is the only version

```yaml
spec:
  versions:
    - name: v1alpha1
      served: true
      storage: true
      deprecated: false
```

At this stage:

- Clients use `example.io/v1alpha1`.
- New and updated objects are stored using the `v1alpha1` schema.
- `status.storedVersions` will include `v1alpha1`.

Example:

```yaml
status:
  storedVersions:
    - v1alpha1
```

---

## 6.2 Stage 2: `v1beta1` is introduced

A conservative rollout first adds `v1beta1` as served without immediately changing storage:

```yaml
spec:
  versions:
    - name: v1alpha1
      served: true
      storage: true
    - name: v1beta1
      served: true
      storage: false
  conversion:
    strategy: Webhook
```

This allows the vendor and platform team to validate:

- The `v1beta1` schema.
- Bidirectional conversion.
- Controller compatibility.
- Client compatibility.
- Webhook availability and certificates.

Both versions are available, but writes continue to use `v1alpha1` storage.

A vendor may introduce `v1beta1` and switch the storage version in the same release. Separating these steps is operationally safer but is not required by Kubernetes.

---

## 6.3 Stage 3: Storage changes to `v1beta1`

The CRD is updated so that `v1beta1` becomes the storage version:

```yaml
spec:
  versions:
    - name: v1alpha1
      served: true
      storage: false
    - name: v1beta1
      served: true
      storage: true
  conversion:
    strategy: Webhook
```

From this point onward:

- New objects are stored as `v1beta1`.
- Any successfully updated object is stored as `v1beta1`.
- Existing objects that have not been updated can remain stored as `v1alpha1`.
- Both API endpoints remain usable.
- The webhook converts between the requested version and the storage version.

During the transition, the CRD can report:

```yaml
status:
  storedVersions:
    - v1alpha1
    - v1beta1
```

This is expected. It indicates that both versions have been used for storage and that old `v1alpha1` encodings might remain.

Changing `storage: true` does not by itself rewrite existing objects.

---

## 6.4 Stage 4: `v1alpha1` is deprecated but still served

The older version is explicitly marked as deprecated:

```yaml
spec:
  versions:
    - name: v1alpha1
      served: true
      storage: false
      deprecated: true
      deprecationWarning: >-
        example.io/v1alpha1 Widget is deprecated; use example.io/v1beta1.
    - name: v1beta1
      served: true
      storage: true
```

At this stage:

- Existing `v1alpha1` manifests can still be submitted.
- API clients receive a warning when using `v1alpha1`.
- `v1alpha1` remains visible in API discovery because `served` is still `true`.
- New and updated resources are stored using `v1beta1`.
- Deprecation does not automatically migrate manifests, clients, or stored objects.
- Deprecation does not stop the controller from reconciling resources.

The purpose of this stage is to provide a compatibility period during which platform teams and tenants update:

- Helm charts.
- Kustomize bases and overlays.
- GitOps repositories.
- Operators and controllers.
- Scripts and automation.
- Generated clients.
- Policy rules and admission controls.
- Documentation and examples.

---

## 7. Why a Status Update Can Migrate an Object

A controller commonly writes observed state to the `/status` subresource:

```text
PATCH /apis/example.io/v1beta1/namespaces/team-a/widgets/example/status
```

Although the desired `spec` might not change, a successful status update is still a persistent API write.

Kubernetes persists the resulting resource using the CRD's current storage version. Therefore, when `v1beta1` is the storage version, a successful status write can cause an object previously encoded as `v1alpha1` to be rewritten using the `v1beta1` storage representation.

### 7.1 Example sequence

1. A `Widget` was originally stored when `v1alpha1` had `storage: true`.
2. The CRD is upgraded so that `v1beta1` has `storage: true`.
3. The controller reconciles the object.
4. The controller updates `.status.conditions`.
5. The API server converts the object to `v1beta1`.
6. The API server persists the updated object using the `v1beta1` storage schema.

### 7.2 Important limitations

A status-driven migration is incidental, not deterministic.

An object might remain stored as `v1alpha1` when:

- It is never reconciled after the storage-version change.
- The controller determines that no status change is required.
- The controller does not use the status subresource.
- The status update fails.
- The controller is stopped or unhealthy.
- The object is effectively dormant.
- Reconciliation reads the object but performs no write.

A `GET`, `LIST`, or `WATCH` can invoke conversion for the response, but it does not rewrite the stored object.

Platform engineers should not rely exclusively on normal reconciliation or status updates to prove that every object has migrated.

---

## 8. Explicit Storage Migration

Before removing the old version, all objects should be deliberately rewritten using the new storage version.

Possible approaches include:

- Kubernetes Storage Version Migration, where supported and enabled.
- A vendor-provided migration job.
- A controlled read-and-replace process.
- A purpose-built migration controller.
- Reapplying or patching every object in a controlled manner.

The migration process must produce an actual write for every object. Merely retrieving the objects is insufficient.

### 8.1 `status.storedVersions`

The CRD records storage-version history:

```yaml
status:
  storedVersions:
    - v1alpha1
    - v1beta1
```

After all objects have been migrated and the migration process has verified that no old stored objects remain, the old entry can be removed:

```yaml
status:
  storedVersions:
    - v1beta1
```

`status.storedVersions` is a CRD-level migration guard, not a per-object inventory. A normal custom-resource response does not expose the object's underlying storage encoding.

Kubernetes does not permit a version to be removed from `spec.versions` while that version remains in `status.storedVersions`.

---

## 9. Stage 5: `v1alpha1` Becomes Unserved

After clients have migrated, the old version can be disabled:

```yaml
spec:
  versions:
    - name: v1alpha1
      served: false
      storage: false
      deprecated: true
    - name: v1beta1
      served: true
      storage: true
```

At this stage:

- The `v1alpha1` REST endpoint is no longer advertised or accepted.
- `v1alpha1` manifests fail when applied.
- Clients compiled or configured to use `v1alpha1` fail to access the resource.
- Existing custom-resource objects are not deleted.
- Objects can still be accessed through `v1beta1`, provided required conversion remains available.
- The controller continues to operate only if it uses a served API version and can process the stored data.

A typical client-facing error resembles:

```text
no matches for kind "Widget" in version "example.io/v1alpha1"
```

The exact error depends on the client and operation.

### 9.1 Effect on running services

Stopping service of the old API version does not directly terminate application Pods or delete dependent resources.

However, indirect impact can occur when:

- A GitOps controller repeatedly applies a `v1alpha1` manifest and fails.
- A Helm upgrade contains `v1alpha1` resources.
- An operator or automation tool is hard-coded to `v1alpha1`.
- A recovery or disaster-recovery process attempts to recreate resources from old manifests.
- A controller requires conversion but the webhook is unavailable.
- A custom resource must be updated to restore or change the dependent service.

Existing runtime state can therefore continue while management, deployment, and recovery operations fail.

---

## 10. Stage 6: `v1alpha1` Is Removed from the CRD

The final CRD contains only `v1beta1`:

```yaml
spec:
  versions:
    - name: v1beta1
      served: true
      storage: true
```

Removal should occur only after all of the following are true:

1. All clients and manifests use `v1beta1`.
2. `v1alpha1` has been unserved for an appropriate compatibility period.
3. Every stored object has been migrated to `v1beta1`.
4. `status.storedVersions` no longer includes `v1alpha1`.
5. The controller no longer depends on `v1alpha1`.
6. Policies, scripts, and generated clients no longer reference `v1alpha1`.
7. Conversion from `v1alpha1` is no longer needed for stored objects or active clients.

After removing the version:

- Kubernetes no longer recognizes `example.io/v1alpha1`.
- The conversion webhook can remove `v1alpha1` conversion logic.
- Rollback to a controller release requiring `v1alpha1` might no longer be safe.
- Old backup manifests might require conversion before restoration.

---

## 11. End-to-End Lifecycle

```text
Release 1
v1alpha1: served=true,  storage=true,  deprecated=false

        |
        | Add v1beta1 and conversion support
        v

Release 2
v1alpha1: served=true,  storage=true,  deprecated=false
v1beta1:  served=true,  storage=false, deprecated=false

        |
        | Switch storage version
        v

Release 3
v1alpha1: served=true,  storage=false, deprecated=false
v1beta1:  served=true,  storage=true,  deprecated=false

        |
        | Announce migration and mark old version deprecated
        v

Release 4
v1alpha1: served=true,  storage=false, deprecated=true
v1beta1:  served=true,  storage=true,  deprecated=false

        |
        | Migrate clients and rewrite all stored objects
        v

Release 5
v1alpha1: served=false, storage=false, deprecated=true
v1beta1:  served=true,  storage=true,  deprecated=false

        |
        | Verify status.storedVersions contains only v1beta1
        v

Release 6
v1alpha1: removed from spec.versions
v1beta1:  served=true, storage=true
```

The vendor can combine some releases, but the ordering constraints remain important.

---

## 12. Common Misconceptions

### â€œThe manifest uses `v1alpha1`, so the object must be stored as `v1alpha1`.â€

Not necessarily. The API server can accept `v1alpha1`, convert it, and store it as `v1beta1`.

### â€œ`kubectl get` returned `v1beta1`, so the object has been migrated.â€

Not necessarily. Reads are converted to the requested or selected representation and do not rewrite storage.

### â€œChanging `storage: true` migrates all existing objects.â€

No. It affects subsequent creates and updates. Existing objects remain in their previous storage representation until written.

### â€œMarking a version deprecated stops it from working.â€

No. `deprecated: true` adds a warning. The version remains usable while `served: true`.

### â€œSetting `served: false` deletes old objects.â€

No. It disables the REST endpoint for that version. The underlying resources remain.

### â€œThe conversion webhook automatically migrates the cluster.â€

No. It transforms objects when the API server requests conversion. Migration requires writes.

### â€œController status reconciliation guarantees that every object will migrate.â€

No. It can migrate objects when a successful status write occurs, but some objects might not be reconciled or updated.

### â€œOnce the old endpoint is unserved, conversion support can be removed.â€

Not always. Conversion might still be required for objects stored in the old version. Remove conversion support only after storage migration is complete.

---

## 13. Platform Engineering Risks

| Risk | Result |
|---|---|
| Storage version changed without a functioning conversion path | Reads or writes can fail, or fields can be lost. |
| Old version marked unserved before client migration | Helm, GitOps, scripts, controllers, and direct applies using the old version fail. |
| Conversion webhook removed too early | Objects stored in or requested through an older representation may become inaccessible. |
| Migration assumed from `GET` operations | Old storage encodings remain and block safe version removal. |
| Migration assumed from controller reconciliation | Dormant or unchanged objects might never be rewritten. |
| Old version removed while present in `status.storedVersions` | The CRD update is rejected. |
| Schema changes use `strategy: None` | Renamed, removed, or structurally changed fields can be mishandled or pruned. |
| Rollback requirements ignored | A rollback to an older controller might require an API version that is no longer served. |
| Backups contain obsolete manifests | Restore operations fail until manifests are converted to a served version. |

---

## 14. Recommended Platform Procedure

### Before introducing `v1beta1`

- Review schema differences between `v1alpha1` and `v1beta1`.
- Determine whether `None` conversion is safe.
- Test bidirectional webhook conversion when schemas differ.
- Confirm the webhook is highly available and its TLS configuration is valid.
- Inventory all clients that use the CRD.

### After introducing `v1beta1`

- Confirm both versions are served.
- Confirm API discovery exposes both versions.
- Test create, read, update, delete, list, watch, and status operations through both versions.
- Confirm values survive round-trip conversion.
- Update platform-owned manifests and generated clients.

### When changing the storage version

- Confirm exactly one version has `storage: true`.
- Confirm the webhook can convert old objects to the new storage version.
- Observe `status.storedVersions`.
- Initiate an explicit rewrite of every object.
- Do not treat reads as migration.

### During deprecation

- Set `deprecated: true` while keeping the version served.
- Provide a clear `deprecationWarning`.
- Establish a migration deadline.
- Detect remaining old-version requests through API audit logs, API server metrics, or other available telemetry.
- Update tenant examples, Helm charts, policies, and documentation.

### Before setting `served: false`

- Confirm active clients no longer request the old version.
- Validate GitOps reconciliation and Helm upgrades with the new version.
- Test disaster recovery and backup restoration.
- Confirm controllers use the new version.
- Have a rollback procedure that can temporarily restore `served: true`.

### Before removing the old version

- Confirm all objects have been rewritten.
- Confirm the old version is absent from `status.storedVersions`.
- Confirm no clients require the old endpoint.
- Confirm old conversion logic is no longer required.
- Review rollback compatibility before installing the release that removes the version.

---

## 15. Useful Inspection Commands

### Display version configuration

```bash
kubectl get crd widgets.example.io \
  -o jsonpath='{range .spec.versions[*]}{.name}{"\tserved="}{.served}{"\tstorage="}{.storage}{"\tdeprecated="}{.deprecated}{"\n"}{end}'
```

### Display recorded storage versions

```bash
kubectl get crd widgets.example.io \
  -o jsonpath='{.status.storedVersions}{"\n"}'
```

### Display conversion configuration

```bash
kubectl get crd widgets.example.io \
  -o yaml
```

Review:

```yaml
spec:
  conversion:
  versions:
status:
  storedVersions:
```

### Test whether a version is served

```bash
kubectl get --raw \
  /apis/example.io/v1alpha1/namespaces/default/widgets
```

```bash
kubectl get --raw \
  /apis/example.io/v1beta1/namespaces/default/widgets
```

These commands test endpoint availability. They do not reveal the per-object storage encoding.

---

## 16. Key Takeaways

1. A CRD may serve multiple API versions, but exactly one is the current storage version.
2. Served versions are client interfaces; the storage version is the persistent representation used for writes.
3. Deprecation warns clients but does not disable the API.
4. A conversion webhook enables different schemas to represent the same logical resource.
5. Changing the storage version does not rewrite existing objects.
6. Any successful persistent update, including a status update, can rewrite an object into the current storage version.
7. Reads and watches do not migrate stored objects.
8. Marking a version unserved breaks clients using that endpoint but does not delete existing resources.
9. Storage migration must be completed before the old version is removed.
10. Conversion support must remain until neither clients nor stored objects require the old representation.

---

## 17. References

- [Kubernetes: Versions in CustomResourceDefinitions](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
- [Kubernetes: Storage Versions](https://kubernetes.io/docs/concepts/overview/working-with-objects/storage-version/)
- [Kubernetes API Reference: CustomResourceDefinition](https://kubernetes.io/docs/reference/kubernetes-api/apiextensions/custom-resource-definition-v1/)
- [Kubernetes: Storage Version Migration](https://kubernetes.io/docs/tasks/manage-kubernetes-objects/storage-version-migration/)