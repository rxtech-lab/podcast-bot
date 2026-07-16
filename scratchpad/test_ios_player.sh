#!/bin/zsh
cd /Users/qiweili/Desktop/rxlab/debate-bot/iOS
xcodebuild -project iOS.xcodeproj -scheme iOS -testPlan iosUnitTestPlan \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro Max' \
  -only-testing:iOSTests/iOSTests \
  test CODE_SIGNING_ALLOWED=NO 2>&1 | grep -E "Test Case|Test Suite|passed|failed|error:" | tail -40
