# KubeVirt Storage Configuration for GCP

This configuration enables key KubeVirt capabilities like Live Migration and performing more than 6 restores per hour from a single snapshot on Google Cloud Platform (GCP). It uses a VolumeSnapshotClass with the `snapshot-type: images` parameter.

## Why This Configuration is Needed

In KubeVirt, VMs are efficiently created by restoring VM disks from image snapshots.

On GCP, without this configuration you cannot simultaneously:
- Restore image snapshots to ReadWriteMany (RWX) PVCs (required for Live Migration)
- Perform more than 6 restores per hour from a single snapshot

This configuration enables both capabilities. 

## Prerequisites

- OpenShift Virtualization deployed on GCP
- GCP PD CSI driver installed and configured
- Hyperdisk StorageClass available
- Cluster admin access to create and modify StorageClass, VolumeSnapshotClass, StorageProfile, and HyperConverged resources

## Configuration Approach

This configuration creates dedicated storage resources for images. Your existing default storage configuration for VM operations remains unchanged.

**After configuration, your cluster will have:**

- A new StorageClass `cnv-images` (RWO only) for when importing images
- A new VolumeSnapshotClass `cnv-images` with `snapshot-type: images`
- An automatically created StorageProfile `cnv-images` that you will configure with RWO access modes and point to the `cnv-images` VolumeSnapshotClass
- Your original default StorageClass and VolumeSnapshotClass will stay unchanged

## Configuration Steps

### Step 1: Create a StorageClass for Golden Images

Duplicate your existing GCP Hyperdisk StorageClass and create `cnv-images`.

1. Get your existing StorageClass:
   ```bash
   oc get storageclass <your-existing-storageclass-name> -o yaml
   ```

2. Create a new file with:
   - `name: cnv-images`
   - Ensure `cnv-images` is NOT set as the default StorageClass (either omit default class annotations or set them to `"false"`)
   - Remove auto-generated fields like `resourceVersion`, `uid`, `creationTimestamp`, `generation`, and `managedFields`

Example:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: cnv-images
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
    storageclass.kubevirt.io/is-default-virt-class: "false"
allowVolumeExpansion: true
parameters:
  type: hyperdisk-balanced
provisioner: pd.csi.storage.gke.io
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

3. Apply:
   ```bash
   oc apply -f <your-file.yaml>
   ```

### Step 2: Create a VolumeSnapshotClass with Special Parameter

Duplicate your existing VolumeSnapshotClass and create `cnv-images`.

1. Get your existing VolumeSnapshotClass:
   ```bash
   oc get volumesnapshotclass <your-existing-vsc-name> -o yaml
   ```

2. Create a new file with:
   - `name: cnv-images`
   - Add `snapshot-type: images` under `parameters`
   - Ensure `cnv-images` is NOT set as the default VolumeSnapshotClass (either omit default class annotations or set them to `"false"`)
   - Remove auto-generated fields like `resourceVersion`, `uid`, `creationTimestamp`, `generation`, and `managedFields`

Example:

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: cnv-images
  annotations:
    snapshot.storage.kubernetes.io/is-default-class: "false"
deletionPolicy: Delete
driver: pd.csi.storage.gke.io
parameters:
  snapshot-type: images
```

3. Apply:
   ```bash
   oc apply -f <your-file.yaml>
   ```

### Step 3: Configure StorageProfile

KubeVirt automatically creates a StorageProfile after Step 1. Configure it to use RWO access modes and the `cnv-images` VolumeSnapshotClass:

1. Edit:
   ```bash
   oc edit storageprofile cnv-images
   ```

2. In the `spec` section:
   - Copy the `claimPropertySets` structure from the `status` section, but keep only ReadWriteOnce access modes for both Block and Filesystem volume modes, and remove ReadWriteMany (RWX) if present
   - Set `snapshotClass: cnv-images`

Example `spec` section:

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: StorageProfile
metadata:
  name: cnv-images
spec:
  claimPropertySets:
  - accessModes:
    - ReadWriteOnce
    volumeMode: Block
  - accessModes:
    - ReadWriteOnce
    volumeMode: Filesystem
  snapshotClass: cnv-images
```

### Step 4: Update HyperConverged CR for Golden Images

Configure `dataImportCronTemplates` to use `cnv-images` StorageClass.

1. Edit (replace `<namespace>` with your namespace, typically `kubevirt-hyperconverged` or `openshift-cnv`):
   ```bash
   oc edit hyperconverged kubevirt-hyperconverged -n <namespace>
   ```

2. In the `spec` section:
   - If `dataImportCronTemplates` are in `status` but not in `spec`, copy them to `spec.dataImportCronTemplates`
   - Set `storageClassName: cnv-images` for each template in `spec.dataImportCronTemplates`:

```yaml
apiVersion: hco.kubevirt.io/v1beta1
kind: HyperConverged
metadata:
  name: kubevirt-hyperconverged
spec:
  dataImportCronTemplates:
  - metadata:
      name: <your-os-image-name>
    spec:
      template:
        spec:
          source:
            registry:
              url: <your-image-url>
          storage:
            storageClassName: cnv-images  # Set this field to cnv-images for each template
            resources:
              requests:
                storage: <size>
```

### Step 5: Delete Existing Snapshots to Trigger Re-import

Now that the configuration is complete, delete existing golden image snapshots to trigger re-import with the new `cnv-images` StorageClass and VolumeSnapshotClass:

1. List snapshots:
   ```bash
   oc get volumesnapshot --all-namespaces --selector cdi.kubevirt.io/dataImportCron
   ```

2. Delete:
   ```bash
   oc delete volumesnapshot --all-namespaces --selector cdi.kubevirt.io/dataImportCron
   ```

DataImportCron will automatically trigger a new import using the new configuration.

### Step 6: Verify the Configuration

After golden images are re-imported, verify:

1. Snapshots use `cnv-images`:
   ```bash
   oc get volumesnapshot --all-namespaces -o yaml | grep snapshotClassName
   ```

2. StorageProfile is configured:
   ```bash
   oc get storageprofile cnv-images -o yaml
   ```
   Confirm:
   - Only ReadWriteOnce access modes are present in `spec.claimPropertySets`
   - `spec.snapshotClass` is set to `cnv-images`

3. VM disks use default StorageClass:
   ```bash
   oc get pvc -n <vm-namespace>
   ```
   VM PVCs should show the default StorageClass, not `cnv-images`.

## How It Works

1. Images are imported as RWO PVCs using the `cnv-images` StorageClass
2. Snapshots are created with `snapshot-type: images`, enabling:
   - Restoring from RWO snapshots to RWX PVCs (for Live Migration)
   - More than 6 restores per hour from a single snapshot
3. When VMs are created, snapshots are restored to RWX volumes using the default StorageClass

## Important Considerations

- Do not set `cnv-images` StorageClass or VolumeSnapshotClass as default classes
- Regular VM operations continue using the original default StorageClass and VolumeSnapshotClass
- VM disks created from images use the default StorageClass (typically RWX), not `cnv-images`
- Snapshots of VM disks use the default VolumeSnapshotClass (without `snapshot-type: images`), not `cnv-images`
  - The default VolumeSnapshotClass is limited to 6 restores per hour from a single snapshot
- Golden image imports (via DataImportCron) use the `cnv-images` StorageClass

## Additional Notes

- **Custom Image Uploads:**
  - For custom images uploaded (not from golden images), use the `cnv-images` StorageClass to ensure snapshots can be restored more than 6 times per hour
  - Using `cnv-images` StorageClass stores the uploaded image as a RWO PVC, and snapshots created from this PVC will use the `cnv-images` VolumeSnapshotClass (with `snapshot-type: images`)
  - These snapshots can be restored to RWX PVCs and support more than 6 restores per hour
  - If you upload custom images using a different StorageClass, they will **not** benefit from these capabilities and will be subject to the 6 restores per hour limitation

- **After Setup:** Once golden images are re-imported with the correct configuration, the StorageProfile's explicit `snapshotClass` setting ensures they continue using the `cnv-images` VolumeSnapshotClass.

## Troubleshooting

### Snapshots still using old VolumeSnapshotClass after re-import

**Symptom:** After completing the configuration, snapshots show a different VolumeSnapshotClass than `cnv-images`.

**Solution:**
1. Verify the StorageProfile `cnv-images` has `spec.snapshotClass: cnv-images`:
   ```bash
   oc get storageprofile cnv-images -o yaml | grep snapshotClass
   ```
2. If missing or incorrect, update the StorageProfile as described in Step 3.
3. Delete existing snapshots again to trigger re-import with the correct configuration.

### Still hitting 6 restores per hour limitation

**Symptom:** Creating multiple VMs from the same golden image fails after 6 restores per hour.

**Solution:**
1. Verify snapshots use `cnv-images` VolumeSnapshotClass:
   ```bash
   oc get volumesnapshot --all-namespaces -o yaml | grep snapshotClassName
   ```
2. Verify the VolumeSnapshotClass has `snapshot-type: images`:
   ```bash
   oc get volumesnapshotclass cnv-images -o yaml | grep snapshot-type
   ```
3. If either is incorrect, review Steps 2 and 3 to ensure proper configuration.

### StorageProfile not automatically created

**Symptom:** After creating the `cnv-images` StorageClass, no StorageProfile is created.

**Solution:**
1. Wait a few minutes for KubeVirt to detect the new StorageClass.
2. Check if StorageProfile exists:
   ```bash
   oc get storageprofile cnv-images
   ```
3. If it doesn't exist, verify the StorageClass was created correctly and check KubeVirt operator logs.

### Golden images not re-importing after deleting snapshots

**Symptom:** After deleting snapshots in Step 5, golden images don't re-import.

**Solution:**
1. Check DataImportCron status:
   ```bash
   oc get dataimportcron -A
   ```
2. Verify the HyperConverged CR has `storageClassName: cnv-images` in `spec.dataImportCronTemplates`.
3. Check DataImportCron events for errors:
   ```bash
   oc describe dataimportcron <name> -n <namespace>
   ```

### VM creation fails with storage errors

**Symptom:** VMs fail to create with errors related to storage or snapshots.

**Solution:**
1. Verify VM disks are using the default StorageClass (not `cnv-images`):
   ```bash
   oc get pvc -n <vm-namespace>
   ```
2. Check that the default StorageClass supports RWX access mode (required for Live Migration).
3. Verify the golden image snapshot exists and is ready:
   ```bash
   oc get volumesnapshot --all-namespaces
   ```

