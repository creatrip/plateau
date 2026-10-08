# Plateau 작업 지침

Plateau는 macOS의 단일 Lima 호스트에서 Incus Debian 시스템 컨테이너를 관리하는 CLI입니다. 이 파일은 저장소 전체에 적용하며, 상세 개발 절차는 [기여 안내](CONTRIBUTING.md)를 따릅니다.

## 필요한 문서

- 프로젝트 개요와 설치는 [README](README.md)를 읽습니다.
- 사용자 명령과 동작을 바꾸면 [README의 사용 안내](README.md#사용-안내)를 갱신합니다.
- [recipes/](recipes/)를 수정할 때는 파일 상단의 사용 방법과 [레시피 관리](CONTRIBUTING.md#레시피-관리)를 확인합니다.
- 구현을 바꾸기 전에는 아래 아키텍처·설계 원칙과 해당 코드를 확인합니다. 설계 선택을 바꾸면 이 파일의 현재 규칙과 선택 이유도 갱신합니다.

## 작업 규칙

- 문서는 한국어로 작성하고 명령어·경로·식별자는 원문을 유지합니다. 이모지와 이모티콘은 사용하지 않습니다.
- Git 작업은 `main` 브랜치에서만 수행합니다. 별도 작업 브랜치나 PR을 만들지 않으며, 검증한 변경을 `main`에 직접 커밋·푸시합니다.
- Go와 `gofmt` 작업은 `mise run`으로 실행하며, 필요한 작업은 `mise.toml`에 등록합니다. GitHub Actions나 PR 자동화는 추가하지 않습니다.
- 함수 추출은 서로 다른 함수 정의 두 곳 이상에서 실제로 공유할 때만 허용합니다. 한 함수에서만 쓰는 로직은 그 함수 안에 둡니다.
- 기존 사용자 데이터와 동시 작업을 보존합니다. 실제 실행 검증에는 별도로 만든 임시 컨테이너를 사용합니다.
- Incus를 상태의 기준으로 삼고 소유권 검증 없이 컨테이너를 변경하지 않습니다. 조회 실패를 누락·중지로 취급하거나 저장소 재초기화의 근거로 삼지 않습니다.
- 공개 명령·실행 기반·네트워크 노출·자동 시작 정책 변경은 요청 범위를 확인하고 [기여 안내](CONTRIBUTING.md)의 변경 절차를 따릅니다.
- 동작 변경은 재현 테스트와 `mise run check`, 패키징 변경은 `mise run package-test`로 검증합니다. 문서 변경은 링크·명령·구현과의 일치를 확인합니다.
- 완료 보고에는 수행한 검사와 미검증 범위를 밝히고, 코드·커밋·원격 반영·릴리스 게시·실환경 검증을 구별합니다.
- 공개 문서와 테스트에는 중립적인 예시를 사용합니다. 실제 업무·고객 이름, 사용자 식별 정보, 화면·로그·백업과 날짜별 운영 일지는 저장소에 넣지 않습니다. 상세 검증 자료는 저장소 밖에 두며 과거 수치를 현재 코드의 검증 결과로 사용하지 않습니다.

## 아키텍처와 코드 책임

```text
macOS
└─ Lima VZ VM: plateau-host (Debian 13 arm64)
   └─ Incus LTS
      ├─ 비특권 Debian 시스템 컨테이너: work-a
      └─ 비특권 Debian 시스템 컨테이너: work-b
```

고정된 호스트 VM 하나가 Linux 커널을 제공하고 컨테이너마다 사용자 공간·systemd·브라우저 데이터가 분리됩니다. 컨테이너별 VM·커널과 Incus VM은 사용하지 않습니다. 호스트 장애는 모든 컨테이너에 영향을 주며, 공유 커널의 격리는 별도 VM의 커널 격리보다 약합니다.

`cmd/plateau`가 구현체를 조립하고 `adapter/cli → application → domain`으로 의존합니다. `application`은 인터페이스를 통해 어댑터를 사용하며 구체적인 Lima·Incus 명령을 알지 않습니다. `domain`에는 프로세스·파일시스템·Cobra·Lima·Incus 의존성을 넣지 않습니다. CLI 입력 검증은 의존성 준비보다 먼저 수행합니다.

| 코드 | 책임 |
| --- | --- |
| [cmd/plateau/main.go](cmd/plateau/main.go) | 구현체 조립, 취소 신호와 종료 코드 |
| [internal/adapter/cli/app.go](internal/adapter/cli/app.go) | 명령·인자 검증, 출력과 원격 셸 종료 코드 |
| [internal/application/bootstrap.go](internal/application/bootstrap.go) | 도구·호스트 준비 순서 |
| [internal/application/instances.go](internal/application/instances.go), [backup.go](internal/application/backup.go) | 수명주기와 백업·복원 순서, 상태 복구와 실패 처리 |
| [internal/domain/instance.go](internal/domain/instance.go) | 이름·포트·상태 값과 오류 구분 |
| [internal/adapter/lima/](internal/adapter/lima/) | Lima 설치·업데이트, 단일 호스트·저장소 준비, 설정과 전송 |
| [internal/adapter/incus/runtime.go](internal/adapter/incus/runtime.go), [inventory.go](internal/adapter/incus/inventory.go) | 컨테이너·포트·VNC, 소유권·목록·준비 상태 |
| [internal/adapter/incus/rename.go](internal/adapter/incus/rename.go) | 이름 변경 체크포인트, 게스트 호스트명과 SSH 기록 |
| [internal/adapter/incus/status.go](internal/adapter/incus/status.go), [ssh.go](internal/adapter/incus/ssh.go), [image.go](internal/adapter/incus/image.go) | 디스크 사용량, SSH와 공용 이미지 |
| [internal/adapter/incus/provision.go](internal/adapter/incus/provision.go), [scripts/](internal/adapter/incus/scripts/) | 실행 파일에 포함하는 게스트 설치·갱신 셸 조각 |
| [internal/adapter/incus/bundle.go](internal/adapter/incus/bundle.go) | 백업 형식·검증·전송·가져오기 |
| [internal/adapter/hostlock/lock.go](internal/adapter/hostlock/lock.go), [process/runner.go](internal/adapter/process/runner.go) | OS 프로세스 잠금, 외부 명령·취소·제한시간 |
| [recipes/](recipes/) | 사용자가 컨테이너 안에서 선택해 실행하는 설치·설정 스크립트 |

### 호스트 준비와 저장소

- 도움말·버전 출력은 호스트 준비를 건너뜁니다. 운영 명령은 로컬 설치·설정·서비스를 검사합니다. Lima는 2.2 이상을 요구하며 사용 가능한 설치가 없거나 명시적인 `update`일 때만 원격 안정판을 조회합니다.
- 정상 호스트는 디스크·인스턴스 설정, 읽기 전용 SSH 탐색으로 서비스·Incus·Btrfs·압축 상태, macOS 자동 시작 등록을 확인합니다. 준비 완료를 로컬 파일에 영구 캐시하지 않습니다. 명시적인 누락·비활성 상태만 상세 복구로 이어집니다.
- 초기화는 기존 저장소·관리 네트워크·프로필 장치가 없을 때만 허용합니다. 저장소 목록은 `incus storage list --format=json`의 명시적인 빈 배열만 없음으로 인정합니다. 빈 출력·`null`·전송 실패·해석 오류·설정 충돌은 초기화를 허용하지 않습니다.
- 새 호스트는 32 GiB 운영체제 디스크와 별도의 1 TiB 희소 raw 디스크 `plateau-data`를 사용합니다. 빈 데이터 파티션을 확인한 뒤 Incus가 블록 장치를 직접 소유하는 `plateau` Btrfs 풀과 zstd 압축을 구성하고 Lima의 데이터 디스크 자동 포맷을 끕니다. ext4 안의 Btrfs 루프 파일은 사용하지 않습니다.
- 기존 `default` 디렉터리 풀과 데이터는 자동 이전·삭제하지 않습니다. 새 컨테이너는 `plateau` 풀을 명시하고 공용 이미지 블록을 쓰기 시 복사로 공유합니다. 컨테이너별 용량 할당량은 없으며 실제 쓰기는 Mac의 남은 공간에 제한됩니다. 데이터 디스크는 운영체제 디스크와 독립적으로 확장할 수 있습니다.
- 목록은 한 번의 Incus 요청으로 상태를 읽고 별도의 Btrfs 일괄 검사로 전용 참조량을 계산합니다. 공용 이미지 블록은 제외합니다. 용량 계산만 실패하면 상태를 유지하고 `DISK`를 `-`로 표시합니다.

### 공용 이미지와 데스크톱

첫 생성은 빌더에서 Debian stable 패키지를 설치하고 장치 고유 식별자를 제거한 뒤 중지 상태의 `plateau-desktop-v1` 이미지를 게시합니다. 이후 컨테이너는 이를 복제합니다. 업데이트는 이미지의 쓰기 시 복사 복제본에 패키지 갱신을 적용해 별칭을 교체하고 기존 컨테이너도 개별 갱신합니다. 호스트의 `/usr`를 컨테이너에 공유하지 않습니다.

OpenSSH, TigerVNC, noVNC·websockify, Openbox·tint2, Chromium, Xfce Terminal, Thunar, Fcitx 5와 Noto 글꼴을 제공합니다. 전체 Xfce 데스크톱은 설치하지 않습니다. XTerm은 기존 명령·Xresources 호환용으로 유지합니다. 업무 프로그램과 레시피는 CLI 바이너리·공용 이미지에 포함하지 않습니다.

- 관리 서비스·실행기는 갱신하되 `/home/plateau`의 사용자 프로필·메뉴·패널·바로가기·심볼릭 링크를 보존합니다. 기본 사용자 설정은 없을 때만 생성하고 과거 기본 명령과 정확히 일치하는 바로가기만 갱신합니다. 환경 변수는 `/etc/profile.d`로 제공하고 사용자 `.profile`을 덮어쓰지 않습니다.
- `/usr/local/bin/plateau-terminal`은 GTK 입력기 환경을 지정해 Xfce Terminal을 엽니다. Noto Sans Mono 12pt와 Pango의 CJK·컬러 이모지 대체, 256색·True Color를 사용합니다.
- Catppuccin Mocha는 upstream revision `cbc9861bb9c40fad098cf55d4b53879e6f9a737c`의 색상을 유지합니다. 프리셋은 `/usr/share/xfce4/terminal/colorschemes/catppuccin-mocha.theme`, 기본값은 `/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-terminal.xml`, MIT 고지는 `/etc/plateau/catppuccin-license.txt`에 설치합니다. 사용자 Xfconf 설정을 우선하고 실행 중인 창을 강제로 바꾸지 않습니다.
- Xfce 기본 앱과 MIME 연결은 `plateau-browser`로 Chromium 프로필·디버깅 포트를 유지하고 폴더는 Thunar로 엽니다. `xfce4-settings`, `xdg-utils`, `file`을 포함하고 기존 사용자 helper/MIME 설정은 보존합니다. 기본 브라우저 바로가기에 URL 인자 `%U`를 전달합니다.
- VNC의 `PAMName=login`, `libpam-systemd`, `dbus-user-session`으로 로그인 세션과 `/run/user/<UID>/bus`, `XDG_RUNTIME_DIR`, 사용자 서비스 관리자를 준비합니다. 화면·입력기 환경 변수는 명시한 항목만 D-Bus와 사용자 서비스에 전달합니다. Fcitx 5는 같은 사용자 버스를 사용하며 설정이 없을 때만 영문·두벌식과 `Shift+Space`를 구성합니다. noVNC의 로컬 IME나 개별 업무 프로그램 설정은 바꾸지 않습니다.
- XTerm의 `/etc/plateau/Xresources`를 읽고 사용자 `~/.Xresources`를 덧붙입니다. 터미널의 복사·붙여넣기는 `Ctrl+Shift+C/V`, 인터럽트는 `Ctrl+C`를 유지합니다.

### 접속과 인증

VNC는 컨테이너의 `127.0.0.1:6080` → Incus proxy의 VM 루프백 개별 포트 → Lima의 macOS 루프백 전달을 사용합니다. TigerVNC는 공유 화면과 `SecurityTypes None`으로 동작합니다. 루프백 제한을 OS 계정별 인증 보장으로 설명하지 않습니다.

SSH는 macOS OpenSSH가 컨테이너 OpenSSH 서버에 직접 인증합니다. VM 루프백의 별도 Incus proxy가 컨테이너 `127.0.0.1:22`에 연결하고, PTY 없는 Lima `ProxyCommand`와 `nc`로 바이트를 전달합니다. 대화형 셸·PTY는 컨테이너에 속하며 macOS/LAN에 SSH proxy 수신 포트를 열지 않습니다.

소유자 전용 Ed25519 private key와 컨테이너별 known_hosts를 사용합니다. 인증 자료 디렉터리는 0700, private key는 0600입니다. 비밀번호·root 로그인·agent/TCP/X11 전달·터널을 허용하지 않습니다. 새 생성은 SSH 준비를 끝내고 반환하며 과거 구성은 지문 검사 후 보완합니다. 공백이 있는 호스트 키 경로를 인용하고 삭제·이름 변경 시 해당 이름의 기록만 정리합니다.

`plateau` 사용자는 guest 내부의 비밀번호 없는 sudo 권한을 가집니다. 생성·업데이트·SSH 보완에서 `/etc/sudoers.d/90-plateau`를 같은 내용으로 적용하고 구문을 검증하며 다른 sudoers 파일은 유지합니다. guest sudo를 macOS 관리자 권한으로 설명하지 않습니다.

### 상태·소유권과 실패 처리

Incus가 목록·상태의 유일한 기준입니다. `boot.autostart`는 재시작 의사이고 검증한 `user.plateau.*`는 소유권·포트·준비 진행·복원 원본을 나타냅니다. 과거 `instances/*.json`은 보존하지만 읽거나 쓰지 않습니다. 이름 접두사로 소유권을 판단하지 않습니다.

`DesiredState`와 관찰한 `RuntimeStatus`를 구분합니다. `ErrNotFound`, `ErrNotManaged`, `ErrInvalidState`와 전송 오류를 구별합니다. 비소유 컨테이너는 목록에서 제외하고 이름 지정 변경은 거절하며, 잘못된 소유 메타데이터와 알 수 없는 상태는 오류로 처리합니다.

`user.plateau.group`은 단일 태그이고 누락·빈 값은 미지정입니다. 별도 그룹 저장소가 없습니다. 입력·조회 metadata에 같은 UTF-8·64자·공백·제어문자 규칙을 적용합니다. `ls`는 그룹·이름 순이며 미지정을 마지막에 두고 `regroup`으로 변경·해제합니다. 생성 재시도는 생략한 그룹을 유지하고 다른 명시값을 거절합니다.

| 작업 | 보존할 순서와 재시도 규칙 |
| --- | --- |
| 생성 | `pending=create`와 자동 시작 해제를 먼저 기록합니다. 장치·시작·SSH·자동 시작 설정 완료 후 표식을 지웁니다. 같은 미완료 생성만 이어가고 완료된 이름은 거절합니다. |
| 시작 | 준비 상태를 검증하고 시작·SSH·자동 시작 설정을 완료합니다. |
| 중지 | 자동 시작 의사를 먼저 끄고 중지합니다. 중지 실패 후에도 재시도할 수 있게 합니다. |
| 이름 변경 | 중지·자동 시작 해제·소유권·대상 부재를 확인하고 `pending=rename`과 변경 전후 이름·hosts 내용을 먼저 저장합니다. Incus 이름·호스트명·두 이름의 SSH 기록을 갱신한 뒤 확정합니다. 같은 요청으로 이어갑니다. |
| 삭제 | 소유권·실행 상태를 확인하고 실행 중이면 `-f` 없이 거절합니다. 자동 시작 해제·필요한 중지 후 삭제합니다. 삭제 후 로컬 SSH 정리 실패는 같은 `rm`으로 재시도합니다. |
| 백업 | 원래 실행 중인 컨테이너만 일시 중지합니다. 내보내기 성공 여부와 별개로 재시작을 시도하며 양쪽 오류와 재시작 의사를 보존합니다. |
| 업데이트 | 원래 중지된 컨테이너는 작업 중 잠시 시작하고 다시 중지합니다. 갱신·상태 복구 오류를 함께 보고하고 자동 시작 의사를 유지합니다. |

이름 변경 metadata는 중지·자동 시작 해제 상태에서만 유효하며 실제 이름은 변경 전후 중 하나여야 합니다. 완료 전 소유 이름은 변경 전 이름을 유지합니다. 원본 `/etc/hosts`는 UTF-8·1 MiB 이하로 검사하고 해당 이름의 별칭만 바꾼 내용을 JSON으로 기록합니다. 중지 상태의 Incus 파일 API로 hostname·hosts를 root 소유 0644로 씁니다. 완료 시 진행 사본을 지우고 `user.plateau.renamed-from`을 남깁니다. 복원 사본에서는 이 출처와 rename 진행 metadata를 제거합니다. 다른 수명주기 작업으로 미완료 이름 변경을 덮어쓰지 않습니다.

### 백업과 복원

- 새 V2 `.plateau`는 `PLATEAU-INCUS-BUNDLE-V2`, 매니페스트 길이, JSON, SHA-256과 zstd Incus export로 구성합니다. 기존 `PLATEAU-INCUS-BUNDLE-V1` gzip도 복원하며 식별 문자열·JSON 버전이 일치해야 합니다. 선택적 group 필드는 두 Incus YAML과 확장 설정에서 대조하고 과거 누락은 미지정으로 해석합니다.
- `incus export --instance-only --compression='zstd -1'`을 사용하고 호스트 준비 때 zstd를 확인합니다. 루트 파일시스템·설정·설치 프로그램·캐시를 포함하되 snapshot·Lima 호스트·macOS·공유 커널은 포함하지 않습니다. macOS용 압축 도구를 추가하지 않습니다.
- export 본문은 FD 3에서 Lima를 통해 Mac의 작성 중인 백업으로 바로 전송하고 진행은 stderr로 분리합니다. Lima `/tmp`의 export 사본이나 Mac의 추가 압축 사본을 만들지 않지만 Incus 서버 자체 임시 파일은 필요합니다.
- 파일은 소유자 전용이며 덮어쓰지 않습니다. 전송 중 해시를 계산해 예약한 checksum 위치를 채우고 동기화·게시합니다. 백업은 암호화하지 않으며 checksum은 작성자 인증 서명이 아닙니다. 신뢰할 수 있는 백업만 복원합니다.

복원은 다음 순서를 유지합니다.

1. 작은 매니페스트로 잠금 대상·이름 충돌을 확인합니다. 아직 본문 검증이나 컨테이너 변경을 하지 않습니다.
2. 압축 본문을 한 번 읽으며 SHA-256·해제·아카이브 경로·두 YAML을 검증하고 원본 매니페스트·본문 checksum에서 복원 식별자를 계산합니다. V1 재시도 식별자를 유지합니다.
3. 같은 순회에서 비공개 zstd 가져오기 사본에 `boot.autostart=false`, `pending=restore`, 복원 식별자를 기록합니다. 원본·사용자 데이터는 보존하며 압축 스트림과 디코더 선행 읽기가 끝나 checksum이 일치하기 전에는 호스트에 전달하지 않습니다.
4. `incus import -`로 원본 기반 임시 이름에 가져오고 소유권·proxy 대상을 검사한 뒤 목적지 포트를 적용합니다. Lima `/tmp`의 전송 사본은 만들지 않습니다.
5. 검증한 중지 상태에서 원래 이름으로 전환하고 사용자의 재시작 의사에 따라 준비합니다. 같은 원본의 미완료 복원만 이어가며 완료된 컨테이너는 덮어쓰지 않습니다.

원본 기반 임시 이름·소유권·중지 상태·원래 이름·포트·의사·진행 표식·복원 식별자·자동 시작 해제가 모두 유효한 임시 복원만 목록에서 숨깁니다. 조회 실패를 이유로 가져온 데이터를 삭제하지 않습니다. 복원은 이미지의 블록 공유 관계를 보존하지 않습니다.

YAML은 `go.yaml.in/yaml/v3`으로 해석하고 파일마다 1 MiB로 제한합니다. 재사용 buffer로 archive 속성을 보존하며 CLI의 `klauspost/compress`로 가져오기 사본을 압축합니다. encoder는 1 MiB window·최대 4개, decoder는 동시 처리 2개·최대 64 MiB window입니다. 압축 처리량·압축 완료 후 Mac 전송 비율·준비/복원 시간은 stderr, 완료 경로는 stdout으로 출력합니다.

### 잠금과 외부 실행

잠금 순서는 컨테이너 → 포트 할당 → 이미지 또는 인증 자료입니다. 이름 변경의 두 이름은 사전순으로 잠그고 포트 선택부터 Incus 예약까지 할당 잠금을 유지합니다. 이미지 갱신은 이미지 잠금을 해제한 뒤 컨테이너를 방문합니다. 변경 잠금 획득 후 상태를 다시 읽습니다.

호스트 준비 잠금은 컨테이너 작업 전에 해제합니다. 목록과 SSH·VNC 세션은 컨테이너 잠금을 보유하지 않습니다. OS가 프로세스 종료 시 잠금을 해제하며 대기자가 같은 inode를 사용해야 하므로 잠금 파일을 지우지 않습니다.

외부 조회는 30초, 비대화형 변경·전송은 30분, HTTP 다운로드는 10분 제한입니다. 취소는 비대화형 프로세스 그룹에 전달하고 출력 파이프 대기도 제한합니다. 대화형 SSH는 시간 제한 없이 취소만 전달합니다. CLI 종료 뒤 Incus 서버 작업이 계속될 수 있으므로 여러 명령을 원자적 transaction·자동 rollback으로 간주하지 않습니다.

## 설계 선택의 이유

| 선택 | 이유와 유지할 제약 |
| --- | --- |
| 단일 Lima VZ·시스템 컨테이너 | 환경마다 커널·VM 자원을 반복하지 않으면서 Debian 사용자 공간·systemd를 유지합니다. 공유 커널·호스트 장애 범위를 명시합니다. |
| 공용 이미지·직접 Btrfs 블록 장치 | 패키지 재설치·전체 파일 복사를 줄이고 변경되지 않은 블록을 공유합니다. 기존 디렉터리 풀은 보존하고 공유 `/usr`나 ext4 안의 루프 파일로 대체하지 않습니다. |
| Incus 기준 상태·진행 metadata | 로컬 JSON과 실제 상태의 불일치를 피하고 실패 뒤 같은 요청으로 이어갑니다. 조회 오류를 누락·삭제·초기화의 근거로 삼지 않습니다. |
| 단일 그룹 태그·옵션 없는 목록 | 별도 그룹 자원과 정렬 옵션 없이 `create --group`, `regroup`, 그룹·이름순 `ls`로 운영합니다. 이전 `group`/`set-group`·목록 필터 별칭을 추가하지 않습니다. |
| 중지 상태의 이름 변경 | 포트·그룹·데이터를 유지하고 hosts 사본·양쪽 이름 잠금·metadata로 재시도합니다. Incus 이름만 바꾸거나 사용자 파일을 일괄 치환하지 않습니다. |
| Xfce Terminal·Fcitx 5·PAM 사용자 세션 | 글꼴 대체·한글 입력·사용자 서비스·기본 앱 연결을 제공하며 전체 Xfce 설치와 noVNC IME 수정을 피합니다. 기존 사용자 설정을 보존합니다. |
| V2 zstd·단일 순회 복원 | 전체 환경의 독립 복원과 V1 읽기를 유지하면서 반복 checksum 검사·gzip 재압축·중간 전송 사본을 줄입니다. 불신 backup의 실행 안전성을 약속하거나 Btrfs 전용 형식·일부 파일 백업으로 바꾸지 않습니다. |
| 로컬 Universal 패키지·명시적 릴리스 | GitHub Actions·자동 release hook 없이 유지관리자 Mac에서 검증·게시합니다. `.pkg`와 `.sha256`은 릴리스에 두고 Git에 커밋하지 않습니다. Universal 포함 여부와 arm64 운영 지원을 구분합니다. |

버전은 유효한 Git `v0.1.x` 태그가 기준입니다. HEAD에 태그가 있으면 그대로 사용하고 없으면 최대 패치 번호에 1을 더하며 태그가 없으면 `0.1.0`입니다. 일반 커밋·push는 릴리스를 게시하지 않습니다. `mise run release`만 clean `main`·원격 일치·검사·패키지 검증 후 주석 태그와 릴리스를 게시합니다. 제목은 `v0.1.x`, 한국어 본문은 `scripts/release_notes.sh`의 실제 커밋 범위·대상 commit·checksum·서명 상태를 사용합니다. 기존 게시 버전은 다시 빌드하지 않습니다.

설치 패키지는 Creatrip 명의의 `com.creatrip.plateau.cli` 식별자를 사용하고 실행 파일을 `/usr/local/bin/plateau`에 설치합니다. 자체 코드·문서는 Apache License 2.0으로 배포하며 `LICENSE`·`NOTICE`·`THIRD_PARTY_NOTICES.txt`를 `/usr/local/share/plateau`에 함께 설치합니다. Go·의존성·포함 자산을 바꾸면 제3자 고지를 갱신하고 패키지 검사에서 식별자·버전과 고지 원문 일치를 확인합니다. `PLATEAU_INSTALLER_IDENTITY`의 Developer ID Installer 서명은 선택 사항이며 공증 단계는 없습니다. 패키지 구조·checksum·내장 버전·두 아키텍처·소스 설치 실패 보존 검사는 새 Mac 설치나 Intel 운영 성공을 뜻하지 않습니다. 게시 절차는 [기여 안내](CONTRIBUTING.md#유지관리자의-릴리스)를 따릅니다.
