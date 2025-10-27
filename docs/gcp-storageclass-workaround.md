# GCP StorageClass Workaround for KubeVirt

## Overview

This document describes a workaround for KubeVirt deployments on Google Cloud Platform (GCP) to address limitations with ReadWriteMany (RWX) storage snapshots. The workaround enables creating golden images as ReadWriteOnce (RWO) snapshots using a special snapshot type, which can then be restored to RWX Hyperdisks without the previous limitation of 6 restores per hour.

## Background

Previously, there was a limitation where only 6 restores could be performed per single ReadWriteMany image. Google has addressed this by implementing support for:

- **K8s-native Snapshots (via `snapshot-type: images`)** to store OS images from RWO Hyperdisks
- **Restoring of Snapshots** created via `snapshot-type: images` into RWX Hyperdisks
- **Removal of the 6 restores per hour limitation** for RWX drives

This workaround leverages these improvements by configuring golden images to be created as RWO snapshots with the special `snapshot-type: images` parameter, which can then be efficiently restored to RWX volumes when creating VMs.

## Prerequisites

- KubeVirt deployed on GCP
- GCP PD CSI driver installed and configured
- Hyperdisk StorageClass available
- CDI (Containerized Data Importer) installed
- Access to modify HyperConverged CR and StorageProfile resources

## Workaround Steps

### Step 1: Create Additional StorageClass

Create a new StorageClass named `cnv-images` with the same configuration as your existing GCP Hyperdisk StorageClass, but specifically for golden image creation:

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

**Note:** Adjust the `type` parameter to match your existing GCP StorageClass configuration.

### Step 2: Ensure Default VolumeSnapshotClass is Set

Make sure your original VolumeSnapshotClass is annotated as the default:

```bash
kubectl annotate volumesnapshotclass <your-original-vsc-name> \
  snapshot.storage.kubernetes.io/is-default-class=true \
  --overwrite
```

This ensures that regular VM operations continue to use the standard snapshot class, while golden images will use the special `cnv-images` snapshot class (created in the next step).

### Step 3: Create VolumeSnapshotClass with Special Parameter

Create a VolumeSnapshotClass that uses the `snapshot-type: images` parameter. This is the key component that enables RWO snapshots to be restored to RWX volumes:

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

**Note:** 
- Do NOT set this as the default snapshot class (keep `is-default-class: false`)
- The `snapshot-type: images` parameter is critical for this workaround to function

### Step 4: Configure StorageProfile

Once the StorageClass is created, KubeVirt will automatically create a corresponding StorageProfile. You need to modify this StorageProfile to:

1. Remove ReadWriteMany (RWX) access modes
2. Set the VolumeSnapshotClass to use the special snapshot class created in Step 3

Edit the automatically created StorageProfile:

```bash
kubectl edit storageprofile cnv-images
```

Update the empty `spec` section to restrict access modes to ReadWriteOnce only:

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

**Important:** The `snapshotClass` field references the VolumeSnapshotClass created in Step 3.

### Step 5: Update HyperConverged CR DataImportCronTemplates

Update the `HyperConverged` CustomResource to configure `DataImportCronTemplates` to use the `cnv-images` StorageClass for golden image imports:

```bash
kubectl edit hyperconverged kubevirt-hyperconverged -n <namespace>
```

Add or modify the `dataImportCronTemplates` section to specify `storageClassName: cnv-images`:

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
            storageClassName: cnv-images
            resources:
              requests:
                storage: <size>
```

**Note:** Apply this configuration for each OS image you want to import as a golden image.

### Step 6: Remove Existing Golden Image Snapshots

To trigger re-import of golden images using the new configuration, delete the existing VolumeSnapshots. First, identify the namespace where golden images are stored:

```bash
# Find the namespace by checking DataImportCron resources
kubectl get dataimportcron -A

# Alternatively, find it by checking existing VolumeSnapshots
kubectl get volumesnapshots -A | grep -E "NAME|snapshot"
```

The namespace is typically `openshift-virtualization-os-images` on OpenShift or `kubevirt-os-images` on upstream Kubernetes, but may vary based on your deployment.

Once you've identified the namespace (replace `<namespace>` with the actual namespace name), delete all golden image snapshots:

```bash
# Delete all VolumeSnapshots in the golden images namespace
kubectl delete volumesnapshots --all -n <namespace>

# For example, if the namespace is openshift-virtualization-os-images:
# kubectl delete volumesnapshots --all -n openshift-virtualization-os-images
```

**Note:** If you prefer to delete snapshots selectively, you can list them first and then delete specific ones:

```bash
# List snapshots in the namespace
kubectl get volumesnapshots -n <namespace>

# Delete a specific snapshot
kubectl delete volumesnapshot <snapshot-name> -n <namespace>
```

After deletion, the DataImportCron will automatically trigger a new import. This time, the import will:

1. Use the `cnv-images` StorageClass (RWO)
2. Create a snapshot using the `cnv-images` VolumeSnapshotClass (with `snapshot-type: images`)
3. The resulting snapshot can be restored to RWX volumes without the 6 restores per hour limitation

### Step 7: Verify the Workaround

After the golden images are re-imported, verify the setup:

1. **Check that snapshots are created with the correct snapshot class:**
   ```bash
   kubectl get volumesnapshot -o yaml | grep snapshotClassName
   ```
   Should show `cnv-images` for the golden image snapshots.

2. **Verify StorageProfile configuration:**
   ```bash
   kubectl get storageprofile cnv-images -o yaml
   ```
   Confirm that RWX access modes are removed and only RWO modes are present.

3. **Test VM creation:**
   Create multiple VMs (10+) from the same golden image source to verify that the 6 restores per hour limitation is resolved.

4. **Verify VM disks use default StorageClass:**
   When VMs are created from the golden images, their disks should be on the default Hyperdisk StorageClass (RWX), not on `cnv-images`:
   ```bash
   kubectl get pvc -n <vm-namespace>
   ```
   The VM PVCs should show the default StorageClass, while the golden image snapshots use `cnv-images`.

## How It Works

The workaround operates on the following principles:

1. **Golden images are stored as RWO volumes** using the `cnv-images` StorageClass
2. **Snapshots are created with `snapshot-type: images`**, which enables Google's optimized snapshot handling
3. **When VMs are created**, the snapshots are restored to RWX volumes using the default StorageClass
4. **The limitation is bypassed** because the source snapshot is created from RWO with the special snapshot type, not from RWX

## Testing the Workaround

To verify that Google's fix is working:

1. Deploy KubeVirt on GCP as normal
2. Apply the workaround steps above
3. Choose one OS image and ensure it's imported using the `cnv-images` StorageClass
4. Attempt to create 10+ VMs from this single source in parallel
5. If successful, the limitation of 6 restores per hour has been resolved

## Additional Notes

- The `cnv-images` StorageClass should **not** be set as the default StorageClass
- The `cnv-images` VolumeSnapshotClass should **not** be set as the default snapshot class
- Regular VM operations continue to use the default StorageClass and VolumeSnapshotClass
- Only golden image imports use the `cnv-images` StorageClass
- VM disks created from golden images will use the default StorageClass (typically RWX)


