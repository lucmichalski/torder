FROM golang:alpine

RUN apk update && \
    apk add --no-cache cmake make ragel boost boost-dev git bash nano jq gcc g++ pcre2-dev pcre2 pcre pcre-dev sqlite-dev sqlite pkgconfig

WORKDIR /opt
RUN git clone --depth=1 https://github.com/intel/hyperscan && \
    cd hyperscan && \
    mkdir -p build && \
    cd build && \
    cmake -DBUILD_STATIC_AND_SHARED=ON .. && \
    make -j12 && \
    make install

ENV PKG_CONFIG_PATH=$PKG_CONFIG_PATH:/usr/local/lib64/pkgconfig/ \
    LD_LIBRARY_PATH=$LD_LIBRARY_PATH:/usr/local/lib64/

RUN go get -u github.com/flier/gohs/hyperscan

COPY main.go .

RUN go mod init github.com/x0rzkov/hyperscan && \
    go mod tidy && \
    go mod download


CMD ["/bin/bash"]
