## Finding

When Argo CD calculates an application’s resource state, it **does not determine the live API version from**:

* `kubectl.kubernetes.io/last-applied-configuration`
* `metadata.managedFields`
* the CRD’s `status.storedVersions`
* the object’s actual etcd storage version

For a resource that exists in both Git and the cluster, Argo CD normally returns the live object **converted to the API version declared by the target manifest**.

## Code path

### 1. The application controller retrieves managed live objects

During `CompareAppState`, Argo CD renders and normalizes the target manifests and then calls:

```go
liveObjByKey, err :=
    m.liveStateCache.GetManagedLiveObjs(destCluster, app, targetObjs)
```

The controller cache delegates this to the GitOps Engine cluster cache. ([GitHub][1])

Conceptually:

```text
controller/state.go
  CompareAppState()
    └── liveStateCache.GetManagedLiveObjs()
          └── gitops-engine clusterCache.GetManagedLiveObjs()
```

### 2. The cluster cache watches preferred API versions

The cluster cache performs Kubernetes discovery with:

```go
GetAPIResources(c.config, true, ...)
```

The `true` means preferred versions are requested. That ultimately calls:

```go
disco.ServerPreferredResources()
```

Argo CD then creates dynamic clients for those discovered resources and maintains the cache through `LIST` and `WATCH`. ([GitHub][2])

The cached object reference preserves the `apiVersion` returned by that watch:

```go
Ref: kube.GetObjectRef(un)
```

and `GetObjectRef` stores:

```go
APIVersion: obj.GetAPIVersion()
```

However, the cache key deliberately excludes version:

```go
ResourceKey{
    Group,
    Kind,
    Namespace,
    Name,
}
```

Therefore, `v1beta1` and `v1` representations of the same resource resolve to the same cache entry. ([GitHub][2])

### 3. Argo CD matches live and target without using version

For each target object, `GetManagedLiveObjs` calculates the key from:

```go
targetObj.GroupVersionKind().Group
targetObj.GroupVersionKind().Kind
targetObj.GetNamespace()
targetObj.GetName()
```

Because version is omitted from the key, a target such as:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
```

can match a cached live object that was retrieved as:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
```

### 4. The live object is converted to the target version

This is the critical section:

```go
converted, err := c.kubectl.ConvertToVersion(
    managedObj,
    targetObj.GroupVersionKind().Group,
    targetObj.GroupVersionKind().Version,
)
```

If client-side conversion succeeds, the converted object becomes the live object.

If conversion fails, Argo CD performs another Kubernetes `GET`, explicitly using the **target object’s GVK**:

```go
managedObj, err = c.kubectl.GetResource(
    context.TODO(),
    c.config,
    targetObj.GroupVersionKind(),
    managedObj.GetName(),
    managedObj.GetNamespace(),
)
```

The source comment explicitly describes this as the fallback when conversion fails. ([GitHub][3])

For built-in Kubernetes types, `ConvertToVersion` uses Argo CD’s registered Kubernetes runtime scheme. It converts first to the internal representation and then to the requested group/version. ([GitHub][4])

For a custom resource such as an ESO `ExternalSecret`, the type generally will not be registered in that built-in runtime scheme. The practical fallback is therefore the direct Kubernetes `GET` at the target GVK.

### 5. The fallback GET requests the exact target API version

`GetResource`:

1. Creates a discovery client.
2. Looks up the requested GVK.
3. Constructs a dynamic client for that group/version/resource.
4. Calls `GET`.

```go
apiResource, err :=
    ServerResourceForGroupVersionKind(disco, gvk, "get")

resource := gvk.GroupVersion().WithResource(apiResource.Name)

return resourceIf.Get(ctx, name, metav1.GetOptions{})
```

`ServerResourceForGroupVersionKind` specifically queries:

```go
disco.ServerResourcesForGroupVersion(
    gvk.GroupVersion().String(),
)
```

Therefore, this is not “give me the preferred version.” It is “give me this exact requested group/version.” ([GitHub][4])

If that version is served, the API server returns the object in that requested version, performing CRD conversion where necessary.

### 6. Argo CD serializes that converted object as `liveState`

After reconciliation, Argo CD retains the resulting live object in `managedResource.Live`. When generating the managed-resource response, it directly marshals that object:

```go
live := res.Live

data, err := json.Marshal(live)
item.LiveState = string(data)
```

There is no subsequent attempt to discover its etcd storage version. ([GitHub][5])

## ESO example

Assume:

```text
Helm target manifest: external-secrets.io/v1beta1
CRD storage version:  external-secrets.io/v1
Preferred version:    external-secrets.io/v1
v1beta1 served:        true
Conversion webhook:   available
```

The likely sequence is:

```text
Cluster cache LIST/WATCH
    ↓
Receives ExternalSecret as v1
    ↓
Matches it to the v1beta1 target by Group/Kind/Namespace/Name
    ↓
Client-side conversion fails because ExternalSecret is a CRD
    ↓
Argo CD GETs external-secrets.io/v1beta1
    ↓
Kubernetes converts stored v1 → served v1beta1
    ↓
Argo CD LiveState contains apiVersion: external-secrets.io/v1beta1
```

Thus an Argo CD response showing:

```yaml
liveState:
  apiVersion: external-secrets.io/v1beta1
```

**does not mean the resource is stored as `v1beta1`.** It may only mean that the Git target uses `v1beta1`, and Argo CD requested the live representation in the same version for comparison.

## Important distinction: managed state versus resource tree

Argo CD exposes two slightly different version representations:

| Argo CD representation                                        | Version source                                                                                    |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `managed-resources[].liveState.apiVersion`                    | Usually normalized to the target manifest’s version                                               |
| Application `status.resources[].version` for a matched target | Usually the normalized live object, and therefore target version                                  |
| Resource-tree node version                                    | API version stored in the cluster cache reference, normally the preferred discovery/watch version |
| Prune-only or orphaned resource with no target                | Can retain the cache/preferred API version                                                        |

The resource tree builds its version from the cache reference’s `APIVersion`, while the cache itself watches preferred API versions. ([GitHub][6])

Consequently, it is possible for the same ESO object to appear as:

```text
Resource tree version:             v1
Managed resource liveState:        v1beta1
Target state:                       v1beta1
Actual etcd storage version:        v1
```

All four can be internally consistent.

## Conclusion

Argo CD obtains the live resource from its Kubernetes cluster cache or through a dynamic-client `GET`, but for desired-versus-live comparison it then deliberately aligns the live object with the target manifest’s API version.

Therefore:

> **Argo CD’s managed live-state `apiVersion` is a comparison representation, not authoritative evidence of the Kubernetes storage version.**

For your ESO migration reporting, Argo CD live state cannot reliably determine whether an object is physically stored as `v1` or `v1beta1`. It is primarily useful for determining which API version the Argo CD target manifest currently declares.

[1]: https://github.com/argoproj/argo-cd/blob/master/controller/state.go "argo-cd/controller/state.go at master · argoproj/argo-cd · GitHub"
[2]: https://github.com/argoproj/argo-cd/blob/master/gitops-engine/pkg/cache/cluster.go "argo-cd/gitops-engine/pkg/cache/cluster.go at master · argoproj/argo-cd · GitHub"
[3]: https://raw.githubusercontent.com/argoproj/argo-cd/master/gitops-engine/pkg/cache/cluster.go "raw.githubusercontent.com"
[4]: https://github.com/argoproj/argo-cd/blob/master/gitops-engine/pkg/utils/kube/ctl.go "argo-cd/gitops-engine/pkg/utils/kube/ctl.go at master · argoproj/argo-cd · GitHub"
[5]: https://github.com/argoproj/argo-cd/blob/master/controller/appcontroller.go "argo-cd/controller/appcontroller.go at master · argoproj/argo-cd · GitHub"
[6]: https://github.com/argoproj/argo-cd/blob/master/controller/cache/cache.go "argo-cd/controller/cache/cache.go at master · argoproj/argo-cd · GitHub"
