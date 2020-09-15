<?php
$config=array(
   'opentoeveryone'=>true,/*是否所有人都能创建评论框*/
   'allowedones'=>array(''),/*如果open to everyone为false，允许创建的人的ID*/
   'maxcontentlength'=>200, /*评论最大长度*/
   'maxcreatenum'=>3, /*最多能创建几个*/
   'commentlistmax'=>5 ,/*每次最多列出几条评论*/
   'replylistmax'=>3, /*每次最多列出几条评论下的回复*/
   'commentcd'=>'100per5', /*评论冷却 单位：分钟，例如5per5就是五条每五分钟*/
   'allowdelete'=>true /*允许用户删除自己发的评论*/
);
?>