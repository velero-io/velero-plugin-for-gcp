# Copyright the Velero contributors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM --platform=$BUILDPLATFORM golang:1.25.10-trixie AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG GOPROXY

ENV GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOARM=${TARGETVARIANT} \
    GOPROXY=${GOPROXY}

COPY . /go/src/velero-plugin-for-gcp
WORKDIR /go/src/velero-plugin-for-gcp
RUN export GOARM=$( echo "${GOARM}" | cut -c2-) && \
    CGO_ENABLED=0 go build -v -o /go/bin/velero-plugin-for-gcp ./velero-plugin-for-gcp && \
    CGO_ENABLED=0 go build -v -o /go/bin/cp-plugin ./hack/cp-plugin

FROM scratch
COPY --from=build /go/bin/velero-plugin-for-gcp /plugins/
COPY --from=build /go/bin/cp-plugin /bin/cp-plugin
USER 65532:65532
ENTRYPOINT ["cp-plugin", "/plugins/velero-plugin-for-gcp", "/target/velero-plugin-for-gcp"]

ARG CREATED
ARG VERSION
ARG GIT_SHA

LABEL \
	org.opencontainers.image.created=${CREATED} \
	org.opencontainers.image.url="https://hub.docker.com/r/velero/velero-plugin-for-gcp" \
	org.opencontainers.image.documentation="https://github.com/vmware-tanzu/velero-plugin-for-gcp" \
	org.opencontainers.image.source="https://github.com/vmware-tanzu/velero-plugin-for-gcp" \
	org.opencontainers.image.version=${VERSION} \
	org.opencontainers.image.revision=${GIT_SHA} \
	org.opencontainers.image.vendor="vmware-tanzu" \
	org.opencontainers.image.licenses="Apache-2.0" \
	org.opencontainers.image.title="velero-plugin-for-gcp" \
	org.opencontainers.image.description="Plugins to support Velero on Google Cloud Platform (GCP)" \
	org.opencontainers.image.base.name="scratch"
