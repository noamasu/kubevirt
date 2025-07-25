package util

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "kubevirt.io/api/core/v1"
	generatedscheme "kubevirt.io/client-go/kubevirt/scheme"
	"kubevirt.io/client-go/log"
)

const (
	ExtensionAPIServerAuthenticationConfigMap = "extension-apiserver-authentication"
	RequestHeaderClientCAFileKey              = "requestheader-client-ca-file"
	VirtShareDir                              = "/var/run/kubevirt"
	VirtImageVolumeDir                        = "/var/run/kubevirt-image-volume"
	VirtKernelBootVolumeDir                   = "/var/run/kubevirt-kernel-boot"
	VirtPrivateDir                            = "/var/run/kubevirt-private"
	KubeletRoot                               = "/var/lib/kubelet"
	KubeletPodsDir                            = KubeletRoot + "/pods"
	HostRootMount                             = "/proc/1/root/"

	NonRootUID        = 107
	NonRootUserString = "qemu"
	RootUser          = 0
)

func IsNonRootVMI(vmi *v1.VirtualMachineInstance) bool {
	_, ok := vmi.Annotations[v1.DeprecatedNonRootVMIAnnotation]

	nonRoot := vmi.Status.RuntimeUser != 0
	return ok || nonRoot
}

func isSRIOVVmi(vmi *v1.VirtualMachineInstance) bool {
	for _, iface := range vmi.Spec.Domain.Devices.Interfaces {
		if iface.SRIOV != nil {
			return true
		}
	}
	return false
}

// Check if a VMI spec requests GPU
func IsGPUVMI(vmi *v1.VirtualMachineInstance) bool {
	if vmi.Spec.Domain.Devices.GPUs != nil && len(vmi.Spec.Domain.Devices.GPUs) != 0 {
		return true
	}
	return false
}

// Check if a VMI spec requests VirtIO-FS
func IsVMIVirtiofsEnabled(vmi *v1.VirtualMachineInstance) bool {
	if vmi.Spec.Domain.Devices.Filesystems != nil {
		for _, fs := range vmi.Spec.Domain.Devices.Filesystems {
			if fs.Virtiofs != nil {
				return true
			}
		}
	}
	return false
}

// Check if a VMI spec requests a HostDevice
func IsHostDevVMI(vmi *v1.VirtualMachineInstance) bool {
	if vmi.Spec.Domain.Devices.HostDevices != nil && len(vmi.Spec.Domain.Devices.HostDevices) != 0 {
		return true
	}
	return false
}

// Check if a VMI spec requests a VFIO device
func IsVFIOVMI(vmi *v1.VirtualMachineInstance) bool {

	if IsHostDevVMI(vmi) || IsGPUVMI(vmi) || isSRIOVVmi(vmi) {
		return true
	}
	return false
}

func UseLaunchSecurity(vmi *v1.VirtualMachineInstance) bool {
	return IsSEVVMI(vmi) || IsSecureExecutionVMI(vmi)
}

func IsAutoAttachVSOCK(vmi *v1.VirtualMachineInstance) bool {
	return vmi.Spec.Domain.Devices.AutoattachVSOCK != nil && *vmi.Spec.Domain.Devices.AutoattachVSOCK
}

func ResourceNameToEnvVar(prefix string, resourceName string) string {
	varName := strings.ToUpper(resourceName)
	varName = strings.Replace(varName, "/", "_", -1)
	varName = strings.Replace(varName, ".", "_", -1)
	return fmt.Sprintf("%s_%s", prefix, varName)
}

// Checks if kernel boot is defined in a valid way
func HasKernelBootContainerImage(vmi *v1.VirtualMachineInstance) bool {
	if vmi == nil {
		return false
	}

	vmiFirmware := vmi.Spec.Domain.Firmware
	if (vmiFirmware == nil) || (vmiFirmware.KernelBoot == nil) || (vmiFirmware.KernelBoot.Container == nil) {
		return false
	}

	return true
}

// AlignImageSizeTo1MiB rounds down the size to the nearest multiple of 1MiB
// A warning or an error may get logged
// The caller is responsible for ensuring the rounded-down size is not 0
func AlignImageSizeTo1MiB(size int64, logger *log.FilteredLogger) int64 {
	remainder := size % (1024 * 1024)
	if remainder == 0 {
		return size
	} else {
		newSize := size - remainder
		if logger != nil {
			if newSize == 0 {
				logger.Errorf("disks must be at least 1MiB, %d bytes is too small", size)
			} else {
				logger.V(4).Infof("disk size is not 1MiB-aligned. Adjusting from %d down to %d.", size, newSize)
			}
		}
		return newSize
	}

}

func SetDefaultVolumeDisk(spec *v1.VirtualMachineInstanceSpec) {
	diskAndFilesystemNames := make(map[string]struct{})

	for _, disk := range spec.Domain.Devices.Disks {
		diskAndFilesystemNames[disk.Name] = struct{}{}
	}

	for _, fs := range spec.Domain.Devices.Filesystems {
		diskAndFilesystemNames[fs.Name] = struct{}{}
	}

	for _, volume := range spec.Volumes {
		if _, foundDisk := diskAndFilesystemNames[volume.Name]; !foundDisk {
			spec.Domain.Devices.Disks = append(
				spec.Domain.Devices.Disks,
				v1.Disk{
					Name: volume.Name,
				},
			)
		}
	}
}

func CalcExpectedMemoryDumpSize(vmi *v1.VirtualMachineInstance) *resource.Quantity {
	const memoryDumpOverhead = 100 * 1024 * 1024
	domain := vmi.Spec.Domain
	vmiMemoryReq := domain.Resources.Requests.Memory()
	expectedPvcSize := resource.NewQuantity(int64(memoryDumpOverhead), vmiMemoryReq.Format)
	expectedPvcSize.Add(*vmiMemoryReq)
	return expectedPvcSize
}

// GenerateVMExportToken creates a cryptographically secure token for VM export
func GenerateVMExportToken() (string, error) {
	const alphanums = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const tokenLen = 20
	ret := make([]byte, tokenLen)
	for i := range ret {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphanums))))
		if err != nil {
			return "", err
		}
		ret[i] = alphanums[num.Int64()]
	}

	return string(ret), nil
}

// GenerateKubeVirtGroupVersionKind ensures a provided object registered with KubeVirts generated schema
// has GVK set correctly. This is required as client-go continues to return objects without
// TypeMeta set as set out in the following issue: https://github.com/kubernetes/client-go/issues/413
func GenerateKubeVirtGroupVersionKind(obj runtime.Object) (runtime.Object, error) {
	objCopy := obj.DeepCopyObject()
	gvks, _, err := generatedscheme.Scheme.ObjectKinds(objCopy)
	if err != nil {
		return nil, fmt.Errorf("could not get GroupVersionKind for object: %w", err)
	}
	objCopy.GetObjectKind().SetGroupVersionKind(gvks[0])

	return objCopy, nil
}
