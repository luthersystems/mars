#!/bin/bash
set -x
if [ x"$USER_ID" != x"0" ]; then
	echo "default:x:${USER_ID}:${GROUP_ID}:Default User:${HOME}:/bin/bash" >> /etc/passwd
fi
pwconv
grpconv
CMD="PATH=$PATH /opt/mars/terraform.py $@"
su -s "/bin/bash" -c "${CMD}" default 
