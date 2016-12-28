#!/usr/bin/env bash
VERSION=$1

/usr/bin/build.sh
cd /go/src
cp /go/src/github.com/lox/lifecycled/builds/lifecycled-linux-$(uname -m) /go/src/builds/lifecycled
cp /go/src/github.com/lox/lifecycled/init/lifecycled /go/src/etc/init.d/
cp /go/src/github.com/lox/lifecycled/handler.sh /go/src/etc/lifecycled-runner.sh

echo export LIFECYCLE_QUEUE=$LIFECYCLE_QUEUE > /go/src/etc/sysconfig/lifecycled
echo export AWS_REGION=$AWS_REGION >> /go/src/etc/sysconfig/lifecycled

chmod +x ./etc/sysconfig/lifecycled
fpm --verbose  --rpm-os linux -s dir -t deb \
    -n lifecycled  -v ${VERSION} \
    -p /go/src/output/NAME_FULLVERSION_ARCH.TYPE \
    --url=https://github.com/lox/lifecycled \
    --vendor=Lox \
    --description "AWS Lifecycle Management" \
    ./builds/lifecycled=/usr/local/bin/lifecycled \
    ./etc/init.d/=/etc/init.d/ \
    ./etc/sysconfig/=/etc/sysconfig/ \
    ./etc/lifecycled-runner.sh=/etc/lifecycled-runner.sh


fpm --verbose --rpm-os linux -s dir -t rpm \
    -n lifecycled  -v ${VERSION} \
    -p /go/src/output/NAME_FULLVERSION_ARCH.TYPE \
    --url=https://github.com/lox/lifecycled \
    --vendor=Lox \
    --description "AWS Lifecycle Management" \
    ./builds/lifecycled=/usr/local/bin/lifecycled \
    ./etc/init.d/=/etc/init.d/ \
    ./etc/sysconfig/=/etc/sysconfig/ \
    ./etc/lifecycled-runner.sh=/etc/lifecycled-runner.sh
