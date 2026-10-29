# Copyright 2021 Google LLC
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

FROM golang:latest AS builder

WORKDIR /build
COPY . .
RUN mkdir /out

ENV GOOS=linux
ENV GO111MODULE=on

ARG CIT_ARCHES=all
ENV CIT_ARCHES=${CIT_ARCHES}

ARG CIT_SUITES="cvm disk guestagent hostnamevalidation hotattach imageboot licensevalidation livemigrate loadbalancer lssd metadata network packagevalidation security ssh suspendresume vmspec u4c shapevalidation"

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    ./local_build.sh -o /out -s "$CIT_SUITES"

FROM debian:bookworm
RUN apt-get update
RUN apt-get install -y openssh-client ca-certificates
COPY --from=builder /out/* /

ENTRYPOINT ["/manager"]
