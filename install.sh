#!/bin/bash

if ! mkdir xud
then
     echo "xud already installed"
else
    echo "installing xud..."
    if ! git clone https://github.com/ExchangeUnion/xud.git > /dev/null 2>&1; then
       echo "unable to git clone xud"
       exit 1
    fi
    echo "xud downloaded"
    cd xud
    if ! npm install > /dev/null 2>&1 ; then
        echo "unable to npm install xud"
        exit 1
    fi
    echo "xud npm installed"
    if ! npm run compile > /dev/null 2>&1 ; then
        echo "unable to build xud"
        exit 1
    fi
    echo "xud installed"
    cd ..
fi

if ! mkdir lnd
then
     echo "lnd already installed"
else
    echo "installing lnd..."
    if ! git clone -b resolver+simnet-ltcd https://github.com/ExchangeUnion/lnd.git ./lnd > /dev/null 2>&1; then
       echo "unable to git clone lnd"
       exit 1
    fi
    echo "lnd downloaded"
    cd lnd
    if ! make > /dev/null 2>&1 ; then
        echo "unable to make lnd"
        exit 1
    fi
    echo "lnd installed"
fi