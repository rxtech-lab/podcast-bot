#!/bin/zsh
cd /Users/qiweili/Desktop/rxlab/debate-bot/iOS
xcodebuild -project iOS.xcodeproj -scheme iOS -destination 'generic/platform=iOS Simulator' -configuration Debug build CODE_SIGNING_ALLOWED=NO 2>&1 | tail -30
