# GPU nodes

squasharr's transcode pools run on GPU nodes found by a label and fed a GPU
through the vendor's device plugin. This is what a node needs for each class,
and how the owner's `kind-cluster-plex` (one laptop node: an RTX 2070 Max-Q
and a UHD 630 iGPU) is set up, as of 2026-10-01.

What squasharr expects of a node:

| Class | Node label (flag) | Resource a pool pod requests | Pod settings squasharr sets |
| --- | --- | --- | --- |
| `nvidia` | `nvidia.com/gpu.present=true` (`--gpu-node-label-nvidia`) | `nvidia.com/gpu` | `runtimeClassName: nvidia`, `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility` |
| `intel` | `intel.feature.node.kubernetes.io/gpu=true` (`--gpu-node-label-intel`) | `gpu.intel.com/i915` | the render group from `--intel-render-groups` |

A node counts for a class only when it carries the label **and** has the
resource allocatable (`transcodejob.gpuNodes`). Every pool, of every class,
runs the one `transcoder` image ([ADR-0015](adr/0015-no-cuda-image.md)):
NVIDIA's driver libraries are injected by its container runtime; Intel's
media driver (iHD, libva, both QSV runtimes) is in the image, because Intel
injects device nodes only ("firmware and kernel driver need to be on the host,
user-space drivers in the GPU workload containers" -- Intel's
[driver notes](https://intel.github.io/intel-device-plugins-for-kubernetes/cmd/gpu_plugin/driver-firmware.html)).

A profile's `hardware` picks among them: `nvidia` or `intel` pin a class,
`auto` takes the first GPU class with a free slot and overflows to `cpu`, and
`gpu` does the same without the CPU overflow (it waits for a GPU slot). Use
`gpu` to keep both GPUs busy on a backlog without loading the host's CPU with
libx265 encodes; the `--slots` budget (`cpu=2,nvidia=1,intel=1` by default)
caps each class.

## NVIDIA

1. The NVIDIA driver on the host, and the
   [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/)
   in the node (on kind, inside the node container: toolkit 1.20.1 there),
   configured as a containerd runtime named `nvidia`:
   `nvidia-ctk runtime configure --runtime=containerd` wrote
   `/etc/containerd/conf.d/99-nvidia.toml` (legacy mode: no CDI specs).
2. A RuntimeClass `nvidia` with handler `nvidia`.
3. The device plugin, from NVIDIA's Helm chart, run under that class:

   ```bash
   helm repo add nvdp https://nvidia.github.io/k8s-device-plugin
   helm install nvidia-device-plugin nvdp/nvidia-device-plugin --version 0.20.1 \
     -n nvidia-device-plugin --create-namespace --set runtimeClassName=nvidia
   ```

   Its default `envvar` strategy hands each pod the id of its own GPU in
   `NVIDIA_VISIBLE_DEVICES`; no image sets that variable (ADR-0015).
4. The label. kind-cluster-plex has no GPU Feature Discovery, so it was set
   by hand: `kubectl label node <node> nvidia.com/gpu.present=true`.

## Intel

1. On the host: the i915 (or xe) kernel driver and GuC/HuC firmware. An
   integrated GPU of the last several generations has both in a stock kernel.
   The kind node runs privileged, so it sees `/dev/dri`.
2. Node Feature Discovery with Intel's rules, which label a node with an
   Intel GPU `intel.feature.node.kubernetes.io/gpu=true` (and
   `gpu.intel.com/device-id.<class>-<id>.*`), then Intel's GPU device plugin,
   scheduled onto labelled nodes -- the "install with NFD" path, which needs
   no cert-manager (Intel's operator does). From
   [intel-device-plugins-for-kubernetes](https://github.com/intel/intel-device-plugins-for-kubernetes)
   at the release tag:

   ```bash
   V=v0.37.0
   R=https://github.com/intel/intel-device-plugins-for-kubernetes/deployments
   kubectl apply -k "$R/nfd?ref=$V"                                  # NFD v0.17.1
   kubectl apply -k "$R/nfd/overlays/node-feature-rules?ref=$V"       # Intel's NodeFeatureRules
   kubectl create namespace intel-device-plugins
   kubectl apply -n intel-device-plugins -k "$R/gpu_plugin/overlays/nfd_labeled_nodes?ref=$V"
   ```

   The plugin then advertises `gpu.intel.com/i915` (and
   `gpu.intel.com/monitoring`), one container per GPU (`-shared-dev-num=1`),
   and mounts only that GPU's `card*` and `renderD*` into a pod that asks.
3. The render group. The pool pod runs as uid 1000; it can open the render
   node when the node's `renderD*` is world-writable (kind-cluster-plex's is,
   `0666`), or when squasharr is given the node's render GID with
   `--intel-render-groups` (`CLUSTARR_INTEL_RENDER_GROUPS`; 989 on the
   owner's host), or under a container runtime with
   `device_ownership_from_security_context`.

Proven on kind-cluster-plex (2026-10-01): a pod requesting
`gpu.intel.com/i915` received only `card1` and `renderD128` (the RTX is
`card0`/`renderD129`), and the `transcoder` image's
`--self-check=intel --trial` encoded `hevc_qsv` (through `vpp_qsv` to 10-bit)
and `hevc_vaapi` on the UHD 630.

Two limits to know:

- **The xe kernel driver.** Intel GPUs on `xe` (Lunar Lake, Battlemage and
  newer Arc) register `gpu.intel.com/xe`, which a pool pod does not request
  (`pool.GPUResource`), so squasharr finds no Intel node there yet.
- **Several GPUs on a node.** VA-API and legacy QSV open the first render
  node, not the one a pod was given; Intel's plugin docs carry the warning.
  Inside a pool pod the plugin mounts only the assigned GPU, and the engine
  opens the pod's own Intel render node for VA-API
  (`selfcheck.IntelRenderNode`), so a pod sees no other.
