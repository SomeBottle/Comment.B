# Comment.B
![banner](https://s1.ax1x.com/2020/09/16/wcIMSe.png)  
*简单的无数据库评论框系统*

---------------------------
![example](https://s1.ax1x.com/2020/09/16/wcIvXd.png)  
## 想法🤔  
在多说离开后，我的博客暂且用上了卜卜口的<a href='http://comment.moe' target='_blank'>萌评论</a>，奈何后来卜卜口太忙<del>于粘土人</del>没啥时间维护项目了(°ー°〃)，于是我产生了自己专门整个能自己维护的评论框系统的想法（开坑日期2018-11-11<del>，我自己也是老咕咕了</del>）
Comment.B的默认模板风格是仿卜卜口的~    

## 特点💊 
* 模板完全可配置
* 支持局部置顶和全局置顶评论   
* 简单（算不上极简？）  
* 同一个页面可以有多个完全相同的评论框（完全没有用的功能啊喂）  

## 部署📖
**详细请看wiki <(￣︶￣)>**  

## 使用提示💡  
* 在部署完成后一定要先访问后台并登录进行初始化  

## 函数供应💬  
```javascript
String.rpl(origin,to);//用于替换{[origin]}为to  
```
```javascript
String.extract(between);//用于提取{{between}}和{{betweenEnd}}之间的内容  
```
```javascript
CB.const();//用于在页面中调用评论框，对于采用pjax的网站很有用   
```
* 其他模板相关函数请在wiki内查看  

## 引用项目⚙️  
* [rou.js](https://github.com/SomeBottle/rou.js)  

--------------
**Based on Apache-2.0 License**
