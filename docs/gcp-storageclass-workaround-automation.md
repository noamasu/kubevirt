# StorageProfile Annotations for Boot Sources

We wish to automate storage configuration for boot sources (golden images) by adding StorageProfile annotations based on provisioner capabilities, similar to how `minimumSupportedPvcSize` is currently handled.

**Motivation:** On GCP, there is a limitation where only 6 restores per hour can be performed from a single ReadWriteMany (RWX) snapshot. To work around this, golden images must be imported as ReadWriteOnce (RWO) PVCs and snapshotted using a VolumeSnapshotClass with `snapshot-type: images` parameter.

Currently, when a StorageClass has provisioner `pd.csi.storage.gke.io`, CDI automatically adds the `cdi.kubevirt.io/minimumSupportedPvcSize: "4Gi"` annotation to the StorageProfile.

## Proposal

Add two more annotations to StorageProfile for provisioners that support optimized boot source handling (e.g., `pd.csi.storage.gke.io`):

1. **`cdi.kubevirt.io/useReadWriteOnceForBootSources: "true"`**
   - Signals to DataImportCron that golden image PVCs should be created with RWO access mode
   - Does NOT filter RWX from StorageProfile - regular VM operations can still use RWX
   - When DataImportCron creates PVCs for importing golden images, it uses RWO instead of RWX

2. **`cdi.kubevirt.io/snapshotClassForBootSources: "<VolumeSnapshotClass-name>"`**
   - Specifies which VolumeSnapshotClass to use for DataImportCron golden image snapshots
   - Should point to a VolumeSnapshotClass with `snapshot-type: images` parameter

## Implementation

### StorageProfile Controller Changes

Similar to `reconcileMinimumSupportedPVCSize()`, add:

1. **`reconcileUseReadWriteOnceForBootSources()`**:
   - Similar to `GetMinimumSupportedPVCSize()`, use a map `UseReadWriteOnceForBootSourcesByProvisionerKey` in `storagecapabilities` package
   - Map provisioner keys to boolean values (or just use the presence in map as true)
   - For provisioners in the map, if annotation not already present, add `cdi.kubevirt.io/useReadWriteOnceForBootSources: "true"` to StorageProfile annotations
   - Example: `"pd.csi.storage.gke.io/hyperdisk": true` (or just presence in map)

2. **`reconcileSnapshotClassForBootSources()`**:
   - For provisioners in the `UseReadWriteOnceForBootSourcesByProvisionerKey` map
   - Look for VolumeSnapshotClass with:
     - Driver matching the provisioner
     - Parameter `snapshot-type: images` (or provisioner-specific boot source parameter from map)
   - If found, add `cdi.kubevirt.io/snapshotClassForBootSources: "<vsc-name>"` to StorageProfile annotations
   - If multiple found, use the first one or prefer one with matching StorageClass name pattern

3. **DataImportCron PVC Creation**:
   - When creating PVCs for golden image imports, check StorageProfile for `cdi.kubevirt.io/useReadWriteOnceForBootSources` annotation
   - If present, create the import PVC with RWO access mode (instead of defaulting to RWX)
   - Regular VM snapshots continue to use RWX as normal

### DataImportCron Changes

When DataImportCron creates a VolumeSnapshot:
- Check StorageProfile for `cdi.kubevirt.io/snapshotClassForBootSources` annotation
- If present, use that VolumeSnapshotClass
- Otherwise, fall back to existing logic

## Example Flow (GCP)

1. Customer creates StorageClass with provisioner `pd.csi.storage.gke.io`
2. Customer creates VolumeSnapshotClass with `snapshot-type: images` parameter
3. CDI automatically (during StorageProfile reconciliation):
   - Adds `useReadWriteOnceForBootSources: "true"` annotation to StorageProfile
   - Finds VolumeSnapshotClass with `snapshot-type: images` and adds `snapshotClassForBootSources` annotation
   - **Note:** StorageProfile keeps all access modes (RWX, RWO, etc.) - no filtering
4. When DataImportCron creates PVCs for golden images:
   - Checks StorageProfile for `useReadWriteOnceForBootSources` annotation
   - Creates import PVCs with RWO access mode (not RWX)
5. When DataImportCron creates VolumeSnapshots from golden image PVCs:
   - Uses the VolumeSnapshotClass specified in `snapshotClassForBootSources` annotation (with `snapshot-type: images`)
   - Result: Golden image snapshots are created with `snapshot-type: images` parameter, enabling >6 restores/hour
6. Regular VM snapshots (running/stopped VMs):
   - Continue to use RWX access mode and normal VolumeSnapshotClass
   - Not affected by the annotations

## Open Questions

1. **Auto-create VolumeSnapshotClass**: Should CDI automatically create a VolumeSnapshotClass with `snapshot-type: images` parameter if one doesn't exist, and then set the `snapshotClassForBootSources` annotation? Or should we only reference existing VolumeSnapshotClasses that the customer must create manually?
2. **VolumeSnapshotClass naming**: Should we prefer VolumeSnapshotClass with name matching StorageClass name pattern (e.g., `<StorageClass-name>-images`)?
3. **Multiple VolumeSnapshotClasses**: What if multiple VolumeSnapshotClasses with `snapshot-type: images` exist for the same provisioner?
4. **Annotation removal**: What happens if the VolumeSnapshotClass is deleted? Should we remove the annotation or keep it?

W