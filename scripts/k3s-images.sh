#!/bin/bash
set -e -x

cd $(dirname $0)/..

mkdir -p bin

ENV CATTLE_K3S_VERSION v1.20.6+k3s1
curl -sLf https://github.com/rancher/k3s/releases/download/${CATTLE_K3S_VERSION}/k3s-images.txt -o ./k3s-images.txt

if [ -e ./k3s-images.txt ]; then
    images=$(grep -e 'docker.io/rancher/pause' -e 'docker.io/rancher/coredns-coredns' ./k3s-images.txt)
    xargs -n1 docker pull <<< "${images}"
    docker save -o ./k3s-airgap-images.tar ${images}
else
    touch ./k3s-airgap-images.tar
fi
