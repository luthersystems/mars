#!/bin/bash
set -x
if [ x"$USER_ID" != x"0" ]; then
	echo "default:x:${USER_ID}:${GROUP_ID}:Default User:${HOME}:/usr/sbin/nologin" >> /etc/passwd
fi
pwconv
grpconv
CMD="PATH=$PATH /opt/mars/mars.py $@"
su -s "/bin/bash" -c "${CMD}" default 
