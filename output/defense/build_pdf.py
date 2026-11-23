"""Build the printable defense guide from the shared Markdown source."""
from pathlib import Path
import re
import json
from xml.sax.saxutils import escape
from reportlab.pdfgen import canvas
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, Preformatted, Table, TableStyle, KeepTogether
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib import colors
from reportlab.lib.enums import TA_LEFT
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.lib.pagesizes import A4

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / 'output/defense/plan.txt'
DEST = ROOT / 'output/pdf/netdosuga-defense.pdf'
for name, filename in [('Arial','arial.ttf'),('Arial-Bold','arialbd.ttf'),('Arial-Italic','ariali.ttf'),('Consolas','consola.ttf')]:
    pdfmetrics.registerFont(TTFont(name, 'C:/Windows/Fonts/' + filename))
pdfmetrics.registerFontFamily('Arial', normal='Arial', bold='Arial-Bold', italic='Arial-Italic', boldItalic='Arial-Bold')
styles = getSampleStyleSheet()
styles.add(ParagraphStyle(name='BodyRU', fontName='Arial', fontSize=10.5, leading=14, spaceAfter=7))
for name, size in [('TitleRU',22),('H1RU',16),('H2RU',12.5),('H3RU',11)]:
    styles.add(ParagraphStyle(name=name,fontName='Arial-Bold',fontSize=size,leading=size*1.25,spaceBefore=13,spaceAfter=7,keepWithNext=True,textColor=colors.HexColor('#D94B32') if name!='H3RU' else colors.HexColor('#252525')))
styles.add(ParagraphStyle(name='CodeRU',fontName='Consolas',fontSize=8.5,leading=11,spaceAfter=8,backColor=colors.HexColor('#F1F2F3'),borderPadding=7))
styles.add(ParagraphStyle(name='CellRU',fontName='Arial',fontSize=9,leading=12,spaceAfter=0,wordWrap='CJK'))

def inline(text):
    text=escape(text)
    text=re.sub(r'\*\*(.+?)\*\*',r'<b>\1</b>',text)
    text=re.sub(r'`([^`]+)`',r'<font name="Consolas">\1</font>',text)
    return text

class Pages(canvas.Canvas):
    def __init__(self,*args,**kwargs):
        super().__init__(*args,**kwargs); self.states=[]
    def showPage(self):
        self.states.append(dict(self.__dict__)); self._startPage()
    def save(self):
        total=len(self.states)
        for state in self.states:
            self.__dict__.update(state)
            self.setStrokeColor(colors.HexColor('#D94B32')); self.line(45,35,A4[0]-45,35)
            self.setFont('Arial',8); self.setFillColor(colors.HexColor('#666666'))
            self.drawString(45,23,'NETDOSUGA | План защиты')
            self.drawRightString(A4[0]-45,23,f'{self._pageNumber} / {total}')
            super().showPage()
        super().save()

def build():
    guide=json.loads((SOURCE.parent/'guide.json').read_text(encoding='utf-8-sig'))
    raw=guide['plan_markdown']
    raw+='\n\n## Приложение. Каталог команд\n'
    for item in guide['commands']:
        raw+='\nCOMMAND_CARD_START\n'
        raw+='\n### '+item['title']+'\n'
        for key,label in [('purpose','Назначение'),('workdir','Рабочая папка'),('expected','Ожидаемый результат'),('side_effects','Изменения и последствия')]:
            value=item.get(key,'')
            if isinstance(value,list): value='; '.join(map(str,value))
            raw+=f'**{label}:** {value}\n\n'
        raw+='```\n'+item['command']+'\n```\n'
        raw+='COMMAND_CARD_END\n'
    raw=raw.translate(str.maketrans({'\u2011':'-','\u2013':'-','\u2014':'-','\u2192':'->','\u00a0':' '}))
    story=[]; code=[]; in_code=False
    width=A4[0]-90
    def emit_code():
        # Visual wrapping preserves each original command in PDF text without adding shell continuations.
        for line in code:
            wrapped=[]; rest=line
            while pdfmetrics.stringWidth(rest,'Consolas',8.5)>width-16:
                n=max(1,int(len(rest)*(width-16)/pdfmetrics.stringWidth(rest,'Consolas',8.5)))
                split=rest.rfind(' ',0,n+1)
                if split>0: n=split+1
                wrapped.append(rest[:n]); rest=rest[n:]
            wrapped.append(rest)
            story.append(Preformatted('\n'.join(wrapped),styles['CodeRU']))
    table_rows=[]
    def emit_table():
        if not table_rows: return
        rows=[[Paragraph(inline(c),styles['CellRU']) for c in row] for row in table_rows]
        n=len(rows[0]); weights=[max(len(r[i]) for r in table_rows) for i in range(n)]
        weights=[max(12,min(65,w)) for w in weights]
        widths=[width*w/sum(weights) for w in weights]
        table=Table(rows,colWidths=widths,repeatRows=1,hAlign='LEFT')
        table.setStyle(TableStyle([('BACKGROUND',(0,0),(-1,0),colors.HexColor('#E9E9E9')),('VALIGN',(0,0),(-1,-1),'TOP'),('LINEBELOW',(0,0),(-1,0),0.7,colors.HexColor('#D94B32')),('LINEBELOW',(0,1),(-1,-1),0.3,colors.HexColor('#DDDDDD')),('LEFTPADDING',(0,0),(-1,-1),6),('RIGHTPADDING',(0,0),(-1,-1),6),('TOPPADDING',(0,0),(-1,-1),6),('BOTTOMPADDING',(0,0),(-1,-1),6)]))
        story.extend([table,Spacer(1,8)]); table_rows.clear()
    card_start=None
    for line in raw.splitlines():
        if line=='COMMAND_CARD_START': card_start=len(story); continue
        if line=='COMMAND_CARD_END':
            card=story[card_start:]; del story[card_start:]; story.append(KeepTogether(card)); card_start=None; continue
        if not in_code and line.strip().startswith('|'):
            cells=[c.strip() for c in line.strip().strip('|').split('|')]
            if not all(re.fullmatch(r'[:\- ]+',c) for c in cells): table_rows.append(cells)
            continue
        emit_table()
        if line.strip().startswith('```'):
            if in_code: emit_code(); code=[]
            in_code=not in_code; continue
        if in_code: code.append(line); continue
        if not line.strip(): continue
        match=re.match(r'^(#{1,4})\s+(.+)',line)
        if match:
            level=len(match[1]); story.append(Paragraph(inline(match[2]),styles[['TitleRU','H1RU','H2RU','H3RU'][level-1]]))
        else:
            line=re.sub(r'^\s*[-*]\s+','• ',line)
            story.append(Paragraph(inline(line),styles['BodyRU']))
    if code: emit_code()
    emit_table()
    DEST.parent.mkdir(parents=True,exist_ok=True)
    SimpleDocTemplate(str(DEST),pagesize=A4,rightMargin=45,leftMargin=45,topMargin=37,bottomMargin=49,title='Netdosuga: план защиты',author='Netdosuga').build(story,canvasmaker=Pages)
    print(DEST)

if __name__=='__main__': build()
