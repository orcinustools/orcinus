# Node image for `orcinus cluster init/join --gpus` on the docker runtime: the
# stock k3s binaries on Ubuntu with the NVIDIA container toolkit. rancher/k3s has
# no glibc, so the toolkit cannot run in it. k3s finds nvidia-container-runtime
# at start-up and registers the `nvidia` containerd runtime + RuntimeClass; the
# driver and /dev/nvidia* come from the host through CDI (--device nvidia.com/gpu=all).
ARG K3S_IMAGE
FROM ${K3S_IMAGE} AS k3s
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gpg \
 && curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia.gpg \
 && echo 'deb [signed-by=/usr/share/keyrings/nvidia.gpg] https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /' > /etc/apt/sources.list.d/nvidia.list \
 && apt-get update && apt-get install -y --no-install-recommends nvidia-container-toolkit \
 && apt-get purge -y curl gpg && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
COPY --from=k3s /bin /opt/k3s/bin
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/opt/k3s/bin:/opt/k3s/bin/aux
VOLUME /var/lib/kubelet /var/lib/rancher/k3s /var/lib/cni /var/log
ENTRYPOINT ["/opt/k3s/bin/k3s"]
CMD ["agent"]
