#!/bin/bash

docker run -v $PWD:/defs namely/protoc-all -f sfu.proto -l go