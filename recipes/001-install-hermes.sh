#!/usr/bin/env bash
# Hermes 설치 — 새 Plateau 컨테이너에서 plateau 사용자로 한 번 실행합니다.
#
# 사용 방법: 아래 두 명령을 컨테이너 안의 터미널에서 실행합니다.
#   curl -fsSL https://gist.githubusercontent.com/jeonghyeon-net/f13e14d1a46ba6f23f4175fe335cfc2c/raw/setup-hermes-on-plateau.sh -o /tmp/001-install-hermes.sh
#   bash /tmp/001-install-hermes.sh
# 이미 파일을 컨테이너에 저장했다면 해당 디렉터리에서 bash 001-install-hermes.sh로 실행합니다.
#
# 설치 후: 터미널에 다시 접속해 hermes setup으로 모델·인증을 설정하고 hermes로 실행합니다.
# 메신저: hermes gateway setup으로 연결하고 hermes gateway run으로 실행합니다.
# 상시 실행 등록은 hermes gateway install, 이후 버전 업데이트는 hermes update를 사용합니다.
# 마지막에 화면 서비스를 재시작하므로 열려 있던 데스크톱 앱과 화면 연결이 종료될 수 있습니다.
#
# 관리 원본: https://github.com/creatrip/plateau/blob/main/recipes/001-install-hermes.sh
# 위 다운로드 명령은 비공개 저장소에 로그인할 필요가 없는 Gist 사본을 사용합니다.
# sudo가 붙은 명령만 컨테이너의 관리자 권한을 쓰며, Hermes는 현재 사용자에게 설치됩니다.
# 설치가 실패하면 컨테이너를 새로 만드는 전제이므로 복구·재실행 처리는 넣지 않았습니다.

# 1. 명령이 실패하면 이후 설치를 중단합니다.
# -e: 명령 실패 시 중단, -u: 정의하지 않은 변수 사용 시 중단,
# pipefail: curl | bash처럼 명령을 연결했을 때 앞쪽의 다운로드 실패도 감지합니다.
set -euo pipefail

# 2. Hermes 실행과 화면 조작에 필요한 Linux 패키지를 설치합니다.
# apt-get update는 설치 가능한 패키지 목록을 갱신하는 명령입니다.
# DEBIAN_FRONTEND=noninteractive와 -y는 설치 도중 설정·승인 질문을 생략합니다.
# --no-install-recommends는 선택적인 권장 패키지를 생략하며, 필수 의존성은 설치합니다.
#   ca-certificates: HTTPS 서버 인증서 확인 / curl: 설치 파일 다운로드
#   git: Hermes 소스 다운로드와 업데이트 / xz-utils: 압축된 설치 파일 해제
#   build-essential: 컴파일러와 빌드 도구
#   python3-dev, libffi-dev: 일부 Python 의존성을 빌드할 때 필요한 개발 파일
#   ripgrep: 빠른 파일 내용 검색 / ffmpeg: 음성·동영상 처리
#   at-spi2-core, dbus-x11: Linux 앱의 접근성 정보를 주고받는 통신 기능
#   dbus-user-session, libpam-systemd: VNC에서도 사용자 서비스 등록·실행에 필요한 로그인 환경
#   imagemagick: 이미지 처리 / libxi6: X11 화면의 입력 장치를 다루는 라이브러리
sudo apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ca-certificates curl git xz-utils build-essential python3-dev libffi-dev ripgrep ffmpeg at-spi2-core dbus-x11 dbus-user-session libpam-systemd imagemagick libxi6

# 3. Plateau에 이미 있는 Chromium을 지정하고 공식 Hermes 설치기를 실행합니다.
# 브라우저를 추가로 내려받는 대신 /usr/bin/chromium을 사용하도록 전달합니다.
# curl로 받은 설치 스크립트를 |로 bash에 넘깁니다. -s는 표준 입력의 스크립트를 실행하고,
# -- 뒤의 옵션은 공식 설치기에 전달합니다. --skip-setup은 모델·계정 설정을 나중으로 미루고,
# --non-interactive는 사용자 입력이 필요한 설치 단계를 생략합니다.
# 공식 기본 구성대로 설치하며 브라우저·Computer Use·메신저 기능을 제외하지 않습니다.
# Hermes 소스와 실행 환경은 기본적으로 ~/.hermes/hermes-agent에 설치됩니다.
export AGENT_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium
curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash -s -- --skip-setup --non-interactive

# 4. 메신저를 연결하는 백그라운드 프로그램인 Gateway에도 화면 위치를 알려줍니다.
# systemd는 백그라운드 프로그램을 실행·관리하는 Linux 기능입니다.
# 기본 Gateway와 이름이 붙은 프로필별 Gateway에 적용할 추가 설정 파일을 각각 만듭니다.
# hermes-gateway-.service.d는 hermes-gateway-로 시작하는 서비스들의 공통 설정 경로입니다.
# .service.d 안의 설정은 원래 서비스 정의에 덧붙여지며, 이 블록이 Gateway를 실행하지는 않습니다.
# dropin은 파일 경로이고, ${dropin%/*}는 그 파일이 들어갈 디렉터리입니다.
# mkdir -p는 필요한 디렉터리를 만들고, cat부터 SERVICE까지의 내용을 파일에 저장합니다.
#   DISPLAY=:1: Plateau의 가상 데스크톱 화면을 사용합니다.
#   XAUTHORITY=%h/.Xauthority: 화면 서버 접근에 사용하는 인증 파일입니다. %h는 사용자 홈입니다.
#   NO_AT_BRIDGE=0: 접근성 통신을 끄지 않도록 합니다. Qt 앱에는 QT_ACCESSIBILITY=1을 지정합니다.
for unit in hermes-gateway.service hermes-gateway-.service; do
  dropin="$HOME/.config/systemd/user/$unit.d/10-plateau-display.conf"
  mkdir -p "${dropin%/*}"
  cat >"$dropin" <<'SERVICE'
[Service]
Environment=DISPLAY=:1 XAUTHORITY=%h/.Xauthority NO_AT_BRIDGE=0 QT_ACCESSIBILITY=1
SERVICE
done

# 5. SSH 등으로 새로 로그인한 터미널에도 같은 화면 설정을 적용합니다.
# /etc/profile.d의 파일은 로그인 셸이 읽으므로, 터미널에서 실행한 Hermes도 :1 화면을 찾습니다.
# export는 이 터미널에서 실행하는 프로그램에 설정값을 전달한다는 뜻입니다.
# sudo tee는 관리자 소유 파일에 내용을 쓰고, >/dev/null은 쓴 내용을 화면에 다시 출력하지 않습니다.
# <<'PROFILE'부터 PROFILE까지는 파일에 저장할 내용입니다. 이름의 따옴표 덕분에 $HOME은
# 지금 치환되지 않고 파일에 남았다가, 로그인한 사용자나 화면 세션이 파일을 읽을 때 치환됩니다.
sudo tee /etc/profile.d/plateau-hermes-display.sh >/dev/null <<'PROFILE'
export DISPLAY=:1 XAUTHORITY="$HOME/.Xauthority" NO_AT_BRIDGE=0 QT_ACCESSIBILITY=1
PROFILE
# 6. 데스크톱을 시작할 때 접근성 통신도 함께 준비하는 실행 파일을 만듭니다.
# . 명령은 앞에서 만든 화면 설정을 읽습니다. 사용자 서비스와 같은 D-Bus에 연결하고,
# at-spi-bus-launcher는 앱의 버튼·입력창 같은 접근성 정보를 주고받는 통로를 시작합니다.
# &는 접근성 통신을 뒤에서 실행하라는 뜻이고, exec는 기존 Plateau 데스크톱 시작기로 이어갑니다.
# Computer Use가 사용할 화면과 접근성 통신을 같은 데스크톱 세션 안에 준비하는 역할입니다.
sudo tee /usr/local/bin/plateau-hermes-session >/dev/null <<'SESSION'
#!/bin/sh
. /etc/profile.d/plateau-hermes-display.sh
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
dbus-update-activation-environment --systemd DISPLAY XAUTHORITY NO_AT_BRIDGE QT_ACCESSIBILITY
/usr/libexec/at-spi-bus-launcher --launch-immediately --a11y=1 &
exec /usr/local/bin/plateau-session
SESSION
# 0755는 파일 소유자는 읽기·쓰기·실행, 나머지 사용자는 읽기·실행할 수 있는 권한입니다.
sudo chmod 0755 /usr/local/bin/plateau-hermes-session

# 7. VNC가 방금 만든 실행 파일로 데스크톱을 시작하도록 추가 설정을 저장합니다.
# VNC는 컨테이너의 데스크톱 화면을 원격으로 보고 조작할 수 있게 하는 서버입니다.
# 빈 ExecStart=로 기존 시작 명령을 지우고, 다음 줄에 사용할 명령을 지정합니다.
# 기존 화면 번호(:1), 해상도(1440x900), 색상 깊이(24비트), 접속 설정을 유지하며,
# -xstartup만 위에서 만든 plateau-hermes-session으로 연결합니다.
# -localhost yes는 컨테이너 내부 접속만 받고, -SecurityTypes None은 VNC 자체 인증을 쓰지 않습니다.
# 이는 Plateau의 기존 터널 접속 구성을 유지하는 것입니다.
# -fg는 서버가 뒤로 분리되지 않게 하여 systemd가 관리하게 하고, -AlwaysShared는 화면 공유를 허용합니다.
sudo mkdir -p /etc/systemd/system/plateau-vnc.service.d
sudo tee /etc/systemd/system/plateau-vnc.service.d/10-hermes.conf >/dev/null <<'VNC'
[Service]
PAMName=login
Environment=NO_AT_BRIDGE=0 QT_ACCESSIBILITY=1
ExecStart=
ExecStart=/usr/bin/tigervncserver :1 -fg -localhost yes -SecurityTypes None -AlwaysShared -geometry 1440x900 -depth 24 -xstartup /usr/local/bin/plateau-hermes-session
VNC

# 8. 변경한 서비스 설정을 다시 읽고 화면 서버를 재시작해 적용합니다.
# plateau-vnc는 실제 화면 서버, plateau-novnc는 그 화면을 웹에서 볼 수 있게 연결하는 서비스입니다.
# 화면 세션을 재시작하므로 열려 있던 데스크톱 앱과 화면 연결이 종료될 수 있습니다.
sudo systemctl daemon-reload
sudo systemctl restart plateau-vnc.service plateau-novnc.service

# 9. 다시 접속하면 설치기가 추가한 명령 경로와 위의 화면 설정이 적용됩니다.
# hermes setup으로 사용할 모델·인증 등을 설정한 뒤 hermes로 실행합니다.
# Slack 등 메신저 연결은 이후 hermes gateway setup에서 설정합니다.
printf '\n설치 완료. 다시 접속한 뒤 hermes setup, hermes 순서로 실행하세요.\n'
