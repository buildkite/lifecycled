FROM golang:1.6

## Used to build packages
RUN apt-get update
RUN apt-get install ruby2.1 -y
RUN apt-get install rubygems -y
RUN apt-get install ruby2.1-dev -y
RUN apt-get install rpm -y

## SETUP GLIDE
WORKDIR /
RUN wget https://github.com/Masterminds/glide/releases/download/v0.12.2/glide-v0.12.2-linux-386.tar.gz
RUN tar xvf glide-v0.12.2-linux-386.tar.gz
RUN cp linux-386/glide /usr/bin/glide

WORKDIR /go/src/github.com/lox/lifecycled

RUN gem install fpm --no-ri --no-rdoc

WORKDIR /go/src
RUN mkdir builds
RUN mkdir output
RUN mkdir etc
RUN mkdir etc/init.d
RUN mkdir etc/sysconfig

ADD build.sh /usr/bin/build.sh
ADD pkg-builder.sh /usr/bin/pkg-builder.sh
WORKDIR /go/src/github.com/lox/lifecycled

#ENTRYPOINT /usr/bin/build.sh
