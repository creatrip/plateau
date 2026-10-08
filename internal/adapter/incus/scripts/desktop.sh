# Managed launchers and services may be refreshed; files in the user's home
# are defaults only. Never replace a user's profile, menu, or desktop settings.
cat >/usr/local/bin/plateau-browser <<'EOF'
#!/bin/sh
exec chromium --user-data-dir="$HOME/.config/plateau/chromium" --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --no-first-run --no-default-browser-check "$@"
EOF
chmod 0755 /usr/local/bin/plateau-browser
cat >/usr/local/bin/plateau-terminal <<'EOF'
#!/bin/sh
export GTK_IM_MODULE=fcitx XMODIFIERS=@im=fcitx
exec xfce4-terminal --disable-server "$@"
EOF
chmod 0755 /usr/local/bin/plateau-terminal
mkdir -p /usr/share/xfce4/helpers
cat >/usr/share/xfce4/helpers/plateau-browser.desktop <<'EOF'
[Desktop Entry]
Version=1.0
Type=X-XFCE-Helper
Name=Plateau Chromium
Icon=chromium
X-XFCE-Binaries=plateau-browser;
X-XFCE-Category=WebBrowser
X-XFCE-Commands=%B;
X-XFCE-CommandsWithParameter=%B "%s";
EOF
install -d -o plateau -g plateau /home/plateau/.config/xfce4
if [ ! -e /home/plateau/.config/xfce4/helpers.rc ] && [ ! -L /home/plateau/.config/xfce4/helpers.rc ]; then
cat >/home/plateau/.config/xfce4/helpers.rc <<'EOF'
WebBrowser=plateau-browser
TerminalEmulator=xfce4-terminal
FileManager=thunar
EOF
chown plateau:plateau /home/plateau/.config/xfce4/helpers.rc
fi
if [ ! -e /home/plateau/.config/mimeapps.list ] && [ ! -L /home/plateau/.config/mimeapps.list ]; then
cat >/home/plateau/.config/mimeapps.list <<'EOF'
[Default Applications]
x-scheme-handler/http=plateau-browser.desktop
x-scheme-handler/https=plateau-browser.desktop
text/html=plateau-browser.desktop
inode/directory=thunar.desktop
EOF
chown plateau:plateau /home/plateau/.config/mimeapps.list
fi
mkdir -p /etc/xdg/xfce4/xfconf/xfce-perchannel-xml /usr/share/xfce4/terminal/colorschemes
cat >/usr/share/xfce4/terminal/colorschemes/catppuccin-mocha.theme <<'EOF'
[Scheme]
Name=Catppuccin-Mocha
ColorCursor=#f5e0dc
ColorCursorForeground=#11111b
ColorCursorUseDefault=FALSE
ColorForeground=#cdd6f4
ColorBackground=#1e1e2e
ColorSelectionBackground=#585b70
ColorSelection=#cdd6f4
ColorSelectionUseDefault=FALSE
TabActivityColor=#fab387
ColorPalette=#45475a;#f38ba8;#a6e3a1;#f9e2af;#89b4fa;#f5c2e7;#94e2d5;#bac2de;#585b70;#f38ba8;#a6e3a1;#f9e2af;#89b4fa;#f5c2e7;#94e2d5;#a6adc8
EOF
cat >/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-terminal.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-terminal" version="1.0">
  <property name="font-name" type="string" value="Noto Sans Mono 12"/>
  <property name="font-use-system" type="bool" value="false"/>
  <property name="misc-menubar-default" type="bool" value="false"/>
  <property name="misc-default-geometry" type="string" value="100x30"/>
  <property name="color-use-theme" type="bool" value="false"/>
  <property name="color-cursor" type="string" value="#f5e0dc"/>
  <property name="color-cursor-foreground" type="string" value="#11111b"/>
  <property name="color-cursor-use-default" type="bool" value="false"/>
  <property name="color-foreground" type="string" value="#cdd6f4"/>
  <property name="color-background" type="string" value="#1e1e2e"/>
  <property name="color-selection-background" type="string" value="#585b70"/>
  <property name="color-selection" type="string" value="#cdd6f4"/>
  <property name="color-selection-use-default" type="bool" value="false"/>
  <property name="tab-activity-color" type="string" value="#fab387"/>
  <property name="color-palette" type="string" value="#45475a;#f38ba8;#a6e3a1;#f9e2af;#89b4fa;#f5c2e7;#94e2d5;#bac2de;#585b70;#f38ba8;#a6e3a1;#f9e2af;#89b4fa;#f5c2e7;#94e2d5;#a6adc8"/>
</channel>
EOF
mkdir -p /etc/plateau
# Catppuccin xfce4-terminal, cbc9861bb9c40fad098cf55d4b53879e6f9a737c.
cat >/etc/plateau/catppuccin-license.txt <<'EOF'
MIT License

Copyright (c) 2021 Catppuccin

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
EOF
cat >/etc/plateau/Xresources <<'EOF'
XTerm*VT100.translations: #override \n\
    Ctrl Shift <Key>C: copy-selection(CLIPBOARD) \n\
    Ctrl Shift <Key>V: insert-selection(CLIPBOARD)
EOF
cat >/usr/local/bin/plateau-session <<'EOF'
#!/bin/sh
export DISPLAY=:1
export GTK_IM_MODULE=fcitx QT_IM_MODULE=fcitx XMODIFIERS=@im=fcitx
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
dbus-update-activation-environment --systemd DISPLAY GTK_IM_MODULE QT_IM_MODULE XMODIFIERS
xrdb -merge /etc/plateau/Xresources
if [ -f "$HOME/.Xresources" ]; then
  xrdb -merge "$HOME/.Xresources"
fi
fcitx5 -d
exec openbox-session
EOF
chmod 0755 /usr/local/bin/plateau-session
install -d -o plateau -g plateau /home/plateau/.config /home/plateau/.config/openbox /home/plateau/.config/tint2 /home/plateau/.local /home/plateau/.local/share /home/plateau/.local/share/applications
install -d -o plateau -g plateau /home/plateau/.config/fcitx5
if [ ! -e /home/plateau/.config/fcitx5/profile ] && [ ! -L /home/plateau/.config/fcitx5/profile ]; then
cat >/home/plateau/.config/fcitx5/profile <<'EOF'
[Groups/0]
Name=Default
Default Layout=us
DefaultIM=hangul

[Groups/0/Items/0]
Name=keyboard-us
Layout=

[Groups/0/Items/1]
Name=hangul
Layout=

[GroupOrder]
0=Default
EOF
chown plateau:plateau /home/plateau/.config/fcitx5/profile
fi
if [ ! -e /home/plateau/.config/fcitx5/config ] && [ ! -L /home/plateau/.config/fcitx5/config ]; then
cat >/home/plateau/.config/fcitx5/config <<'EOF'
[Hotkey/TriggerKeys]
0=Shift+space
EOF
chown plateau:plateau /home/plateau/.config/fcitx5/config
fi
if [ ! -e /home/plateau/.config/openbox/autostart ] && [ ! -L /home/plateau/.config/openbox/autostart ]; then
cat >/home/plateau/.config/openbox/autostart <<'EOF'
#!/bin/sh
hsetroot -solid '#263238'
tint2 -c "$HOME/.config/tint2/tint2rc" &
EOF
chown plateau:plateau /home/plateau/.config/openbox/autostart
chmod 0755 /home/plateau/.config/openbox/autostart
fi
if [ ! -e /home/plateau/.config/openbox/menu.xml ] && [ ! -L /home/plateau/.config/openbox/menu.xml ]; then
cat >/home/plateau/.config/openbox/menu.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<openbox_menu xmlns="http://openbox.org/3.4/menu">
  <menu id="root-menu" label="Plateau">
    <item label="Terminal">
      <action name="Execute"><command>plateau-terminal</command></action>
    </item>
    <item label="Chromium">
      <action name="Execute"><command>plateau-browser</command></action>
    </item>
    <separator />
    <item label="Reconfigure"><action name="Reconfigure" /></item>
  </menu>
</openbox_menu>
EOF
chown plateau:plateau /home/plateau/.config/openbox/menu.xml
fi
if [ ! -e /home/plateau/.local/share/applications/plateau-terminal.desktop ] && [ ! -L /home/plateau/.local/share/applications/plateau-terminal.desktop ]; then
cat >/home/plateau/.local/share/applications/plateau-terminal.desktop <<'EOF'
[Desktop Entry]
Type=Application
Name=Terminal
Exec=plateau-terminal
Icon=utilities-terminal
Terminal=false
EOF
chown plateau:plateau /home/plateau/.local/share/applications/plateau-terminal.desktop
fi
# Upgrade only the previous stock command, retaining custom launchers and menus.
for launcher in /home/plateau/.config/openbox/menu.xml /home/plateau/.local/share/applications/plateau-terminal.desktop; do
  if [ ! -L "$launcher" ]; then
    updated_launcher=$(mktemp)
    sed -e 's|<command>xterm -fa '\''Noto Sans Mono'\'' -fs 12</command>|<command>plateau-terminal</command>|g' \
        -e 's|^Exec=xterm -fa "Noto Sans Mono" -fs 12$|Exec=plateau-terminal|' "$launcher" >"$updated_launcher"
    if ! cmp -s "$launcher" "$updated_launcher"; then
      cat "$updated_launcher" >"$launcher"
    fi
    rm -f "$updated_launcher"
  fi
done
if [ ! -e /home/plateau/.local/share/applications/plateau-browser.desktop ] && [ ! -L /home/plateau/.local/share/applications/plateau-browser.desktop ]; then
cat >/home/plateau/.local/share/applications/plateau-browser.desktop <<'EOF'
[Desktop Entry]
Type=Application
Name=Chromium
Exec=plateau-browser %U
Icon=chromium
Terminal=false
MimeType=text/html;x-scheme-handler/http;x-scheme-handler/https;
EOF
chown plateau:plateau /home/plateau/.local/share/applications/plateau-browser.desktop
fi
# Preserve custom browser commands; upgrade only the former stock launcher.
browser_launcher=/home/plateau/.local/share/applications/plateau-browser.desktop
if [ ! -L "$browser_launcher" ]; then
  updated_launcher=$(mktemp)
  sed 's|^Exec=plateau-browser$|Exec=plateau-browser %U|' "$browser_launcher" >"$updated_launcher"
  if ! cmp -s "$browser_launcher" "$updated_launcher"; then
    cat "$updated_launcher" >"$browser_launcher"
  fi
  rm -f "$updated_launcher"
fi
update-desktop-database /home/plateau/.local/share/applications
if [ ! -e /home/plateau/.config/tint2/tint2rc ] && [ ! -L /home/plateau/.config/tint2/tint2rc ]; then
cat >/home/plateau/.config/tint2/tint2rc <<'EOF'
# Panel background
rounded = 0
border_width = 0
background_color = #20242b 100
border_color = #20242b 100

panel_monitor = all
panel_position = bottom center horizontal
panel_size = 100% 42
panel_padding = 6 4 6
panel_background_id = 1
panel_items = LTSC
wm_menu = 1

launcher_padding = 4 2 4
launcher_background_id = 1
launcher_icon_size = 28
launcher_item_app = /home/plateau/.local/share/applications/plateau-terminal.desktop
launcher_item_app = /home/plateau/.local/share/applications/plateau-browser.desktop

taskbar_mode = single_desktop
taskbar_padding = 2 2 2
taskbar_background_id = 1
task_icon = 1
task_text = 1
task_centered = 0
task_maximum_size = 220 36
task_padding = 6 2 6
task_background_id = 1

systray_padding = 4 2 4
systray_background_id = 1

time1_format = %H:%M
clock_padding = 8 0
clock_background_id = 1
EOF
chown plateau:plateau /home/plateau/.config/tint2/tint2rc
fi
if [ ! -e /home/plateau/.profile ] && [ ! -L /home/plateau/.profile ]; then
cat >/home/plateau/.profile <<'EOF'
if [ -f "$HOME/.bashrc" ]; then
  . "$HOME/.bashrc"
fi
export DISPLAY=:1
EOF
chown plateau:plateau /home/plateau/.profile
fi
mkdir -p /etc/profile.d
cat >/etc/profile.d/plateau-display.sh <<'EOF'
# Set the virtual desktop for Plateau login shells without editing ~/.profile.
if [ "$(id -un)" = plateau ]; then
  export DISPLAY="${DISPLAY:-:1}"
fi
EOF
cat >/etc/systemd/system/plateau-vnc.service <<'EOF'
[Unit]
Description=Plateau private VNC desktop
After=network.target

[Service]
Type=simple
User=plateau
PAMName=login
Environment=HOME=/home/plateau
ExecStart=/usr/bin/tigervncserver :1 -fg -localhost yes -SecurityTypes None -AlwaysShared -geometry 1440x900 -depth 24 -xstartup /usr/local/bin/plateau-session
ExecStop=/usr/bin/tigervncserver -kill :1
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
cat >/etc/systemd/system/plateau-novnc.service <<'EOF'
[Unit]
Description=Plateau private noVNC gateway
After=plateau-vnc.service
Requires=plateau-vnc.service

[Service]
Type=simple
User=plateau
Environment=HOME=/home/plateau
ExecStart=/usr/bin/websockify --web=/usr/share/novnc 127.0.0.1:6080 127.0.0.1:5901
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
